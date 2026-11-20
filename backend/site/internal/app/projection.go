package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// 订阅事件的载荷（只取用到的字段，见 contracts/events/fleet.md、ride.md）
type vehicleListed struct {
	VehicleID   string  `json:"vehicleId"`
	PlateNo     *string `json:"plateNo"`
	PileTagType *string `json:"pileTagType"`
	PileTag     *string `json:"pileTag"`
}

type vehicleRemoved struct {
	VehicleID string `json:"vehicleId"`
}

type orderUnlocked struct {
	OrderID        string  `json:"orderId"`
	VehicleID      string  `json:"vehicleId"`
	StartStationID *string `json:"startStationId"`
	StartAt        *string `json:"startAt"`
}

type orderEnded struct {
	OrderID        string  `json:"orderId"`
	VehicleID      string  `json:"vehicleId"`
	StartStationID *string `json:"startStationId"`
	EndStationID   *string `json:"endStationId"`
	EndPileID      *string `json:"endPileId"`
	InStation      bool    `json:"inStation"`
	EndAt          *string `json:"endAt"`
}

// eventTime 取载荷里的时刻，没有或解析不了时取信封的发生时刻，再不行取当前时刻
func (a *App) eventTime(primary *string, fallback string) time.Time {
	if primary != nil {
		if t, err := time.Parse(time.RFC3339, *primary); err == nil {
			return t
		}
	}
	if t, err := time.Parse(time.RFC3339, fallback); err == nil {
		return t
	}
	return a.now()
}

// applyProjection 更新本地投影：车辆标识对应、进行中骑行、停放车辆
func (a *App) applyProjection(ctx context.Context, tx pgx.Tx, env Envelope) error {
	switch env.Type {
	case "fleet.vehicle.listed":
		var p vehicleListed
		if err := json.Unmarshal(env.Payload, &p); err != nil || p.VehicleID == "" {
			return fmt.Errorf("%s 载荷无法解析: %v", env.Type, err)
		}
		if p.PileTag == nil || *p.PileTag == "" {
			_, err := tx.Exec(ctx, `DELETE FROM vehicle_tags WHERE vehicle_id=$1`, p.VehicleID)
			return err
		}
		tagType := "RFID"
		if p.PileTagType != nil && *p.PileTagType != "" {
			tagType = *p.PileTagType
		}
		// 同一识别方式内标识不重复：清单以最新为准，旧车让出该标识
		if _, err := tx.Exec(ctx, `DELETE FROM vehicle_tags WHERE tag_type=$1 AND tag=$2 AND vehicle_id<>$3`, tagType, *p.PileTag, p.VehicleID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO vehicle_tags(vehicle_id, plate_no, tag_type, tag, updated_at) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (vehicle_id) DO UPDATE SET plate_no=EXCLUDED.plate_no, tag_type=EXCLUDED.tag_type, tag=EXCLUDED.tag, updated_at=EXCLUDED.updated_at`,
			p.VehicleID, p.PlateNo, tagType, *p.PileTag, a.now())
		return err

	case "fleet.vehicle.removed":
		var p vehicleRemoved
		if err := json.Unmarshal(env.Payload, &p); err != nil || p.VehicleID == "" {
			return fmt.Errorf("%s 载荷无法解析: %v", env.Type, err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM vehicle_tags WHERE vehicle_id=$1`, p.VehicleID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE piles SET ride_left_order=NULL, ride_left_vehicle_id=NULL, ride_left_at=NULL WHERE ride_left_vehicle_id=$1`, p.VehicleID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE piles SET ride_back_order=NULL, ride_back_vehicle_id=NULL, ride_back_at=NULL WHERE ride_back_vehicle_id=$1`, p.VehicleID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM parked_vehicles WHERE vehicle_id=$1`, p.VehicleID)
		return err

	case "ride.order.unlocked":
		var p orderUnlocked
		if err := json.Unmarshal(env.Payload, &p); err != nil || p.OrderID == "" {
			return fmt.Errorf("%s 载荷无法解析: %v", env.Type, err)
		}
		startAt := a.eventTime(p.StartAt, env.OccurredAt)
		// 已先收到结束（重放乱序）则不回退
		_, err := tx.Exec(ctx, `INSERT INTO ride_orders(order_id, vehicle_id, start_station_id, status, start_at) VALUES ($1,$2,$3,'骑行中',$4)
			ON CONFLICT (order_id) DO UPDATE SET start_at = coalesce(ride_orders.start_at, EXCLUDED.start_at)`, p.OrderID, p.VehicleID, p.StartStationID, startAt)
		if err != nil {
			return err
		}
		// 车被骑走：从停放车辆里去掉
		if _, err := tx.Exec(ctx, `DELETE FROM parked_vehicles WHERE vehicle_id=$1`, p.VehicleID); err != nil {
			return err
		}
		if p.VehicleID == "" {
			return nil
		}
		// 这辆车已经开始了更新的一单：本事件是迟到的旧事件，不打临时标记
		var newer bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ride_orders WHERE vehicle_id=$1 AND order_id<>$2 AND start_at > $3)`, p.VehicleID, p.OrderID, startAt).Scan(&newer); err != nil {
			return err
		}
		var ended bool
		if err := tx.QueryRow(ctx, `SELECT status='已结束' FROM ride_orders WHERE order_id=$1`, p.OrderID).Scan(&ended); err != nil {
			return err
		}
		if newer || ended {
			return nil
		}
		// 桩上还记着这辆车（离位报告可能迟到）：临时按"车已离开"处理，除非桩在开锁之后又报告过它
		if _, err := tx.Exec(ctx, `UPDATE piles SET ride_left_order=$1, ride_left_vehicle_id=$2, ride_left_at=$3
			WHERE occupied_vehicle_id=$2 AND (occupied_at IS NULL OR occupied_at <= $3)`, p.OrderID, p.VehicleID, startAt); err != nil {
			return err
		}
		// 再次开锁：这辆车此前"已回来"的临时标记作废
		_, err = tx.Exec(ctx, `UPDATE piles SET ride_back_order=NULL, ride_back_vehicle_id=NULL, ride_back_at=NULL
			WHERE ride_back_vehicle_id=$1 AND (ride_back_at IS NULL OR ride_back_at <= $2)`, p.VehicleID, startAt)
		return err

	case "ride.order.ended":
		var p orderEnded
		if err := json.Unmarshal(env.Payload, &p); err != nil || p.OrderID == "" {
			return fmt.Errorf("%s 载荷无法解析: %v", env.Type, err)
		}
		endAt := a.eventTime(p.EndAt, env.OccurredAt)
		if _, err := tx.Exec(ctx, `INSERT INTO ride_orders(order_id, vehicle_id, start_station_id, end_station_id, status, end_at) VALUES ($1,$2,$3,$4,'已结束',$5)
			ON CONFLICT (order_id) DO UPDATE SET status='已结束', end_station_id=EXCLUDED.end_station_id, end_at=EXCLUDED.end_at`,
			p.OrderID, p.VehicleID, p.StartStationID, p.EndStationID, endAt); err != nil {
			return err
		}
		if !p.InStation || p.EndStationID == nil || *p.EndStationID == "" || p.VehicleID == "" {
			return nil
		}
		if _, err := tx.Exec(ctx, `INSERT INTO parked_vehicles(vehicle_id, station_id, since) VALUES ($1,$2,$3)
			ON CONFLICT (vehicle_id) DO UPDATE SET station_id=EXCLUDED.station_id, since=EXCLUDED.since`, p.VehicleID, *p.EndStationID, endAt); err != nil {
			return err
		}
		if p.EndPileID == nil || *p.EndPileID == "" {
			return nil
		}
		// 这辆车已经开始了更新的一单：本事件是迟到的旧事件，不打临时标记
		var newer bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ride_orders WHERE vehicle_id=$1 AND order_id<>$2 AND start_at > $3)`, p.VehicleID, p.OrderID, endAt).Scan(&newer); err != nil {
			return err
		}
		if newer {
			return nil
		}
		// 桩上没有这辆车的在位记录（入位报告可能迟到）：临时按"车已回来"处理，除非桩在还车之后又报告过别的状态
		if _, err := tx.Exec(ctx, `UPDATE piles SET ride_back_order=NULL, ride_back_vehicle_id=NULL, ride_back_at=NULL WHERE ride_back_vehicle_id=$1 AND pile_id<>$2`, p.VehicleID, *p.EndPileID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE piles SET ride_back_order=$1, ride_back_vehicle_id=$2, ride_back_at=$3,
			ride_left_order = CASE WHEN ride_left_vehicle_id=$2 THEN NULL ELSE ride_left_order END,
			ride_left_at = CASE WHEN ride_left_vehicle_id=$2 THEN NULL ELSE ride_left_at END,
			ride_left_vehicle_id = CASE WHEN ride_left_vehicle_id=$2 THEN NULL ELSE ride_left_vehicle_id END
			WHERE pile_id=$4 AND NOT (occupied AND occupied_vehicle_id=$2) AND (occupied_at IS NULL OR occupied_at <= $3)`,
			p.OrderID, p.VehicleID, endAt, *p.EndPileID)
		return err
	}
	return nil // 未订阅的类型忽略
}
