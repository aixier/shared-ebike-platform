package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"bike/ride/migrations"
)

// Migrate 启动时执行迁移。
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	return migrateDB(ctx, db)
}

func migrateDB(ctx context.Context, db *sql.DB) error {
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.UpContext(ctx, db, ".")
}

// 错误分类。
var (
	errCASConflict = errors.New("订单已被并发修改")
	errNotFound    = errors.New("没有找到")
)

// uniqueViolation 唯一约束冲突，返回约束名。
func uniqueViolation(err error) (string, bool) {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return pe.ConstraintName, true
	}
	return "", false
}

// isConnErr 数据库不可用（连不上、超时），而不是 SQL 本身的错误。
func isConnErr(err error) bool {
	if err == nil {
		return false
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return false
	}
	if errors.Is(err, errCASConflict) || errors.Is(err, errNotFound) || errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	return true
}

// Store 数据库访问。
type Store struct {
	pool *pgxpool.Pool
}

func scanOrderDoc(b []byte, rev int) (*Order, error) {
	var o Order
	if err := json.Unmarshal(b, &o); err != nil {
		return nil, err
	}
	o.Rev = rev
	return &o, nil
}

// getOrder 读一单。
func (st *Store) getOrder(ctx context.Context, id string) (*Order, error) {
	var b []byte
	var rev int
	err := st.pool.QueryRow(ctx, `SELECT doc, rev FROM orders WHERE order_id=$1`, id).Scan(&b, &rev)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return scanOrderDoc(b, rev)
}

func (st *Store) queryOrders(ctx context.Context, sqlText string, args ...any) ([]*Order, error) {
	rows, err := st.pool.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Order
	for rows.Next() {
		var b []byte
		var rev int
		if err := rows.Scan(&b, &rev); err != nil {
			return nil, err
		}
		o, err := scanOrderDoc(b, rev)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// orderCols 由订单得到列值。
func orderCols(o *Order) []any {
	doc, _ := json.Marshal(o)
	return []any{o.OrderID, o.UserID, o.VehicleID, o.PlateNo, o.BrandID, o.OperatorID, o.Status, o.RequestAt, o.StartAt, o.Degraded != nil, o.Rev, doc}
}

const orderInsert = `INSERT INTO orders (order_id,user_id,vehicle_id,plate_no,brand_id,operator_id,status,request_at,start_at,degraded,rev,doc)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`

// saveOrderTx 在一个事务里写订单（新建插入、修改按 rev 比较并交换）与发件表，订单结束时更新车辆最后位置。
// upsert=true 用于本地日志回放：只有新版本才覆盖。
func (st *Store) saveOrderTx(ctx context.Context, o *Order, isNew, upsert bool, evs []Envelope, vehicleLoc *vehicleLocUpdate) error {
	tx, err := st.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := saveOrderIn(ctx, tx, o, isNew, upsert); err != nil {
		return err
	}
	if err := insertOutbox(ctx, tx, evs); err != nil {
		return err
	}
	if vehicleLoc != nil {
		if err := updateVehicleLoc(ctx, tx, vehicleLoc); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func saveOrderIn(ctx context.Context, tx pgx.Tx, o *Order, isNew, upsert bool) error {
	switch {
	case upsert:
		cols := orderCols(o)
		_, err := tx.Exec(ctx, orderInsert+` ON CONFLICT (order_id) DO UPDATE SET status=EXCLUDED.status, start_at=EXCLUDED.start_at,
			degraded=EXCLUDED.degraded, rev=EXCLUDED.rev, doc=EXCLUDED.doc, updated_at=now() WHERE orders.rev < EXCLUDED.rev`, cols...)
		return err
	case isNew:
		_, err := tx.Exec(ctx, orderInsert, orderCols(o)...)
		return err
	default:
		old := o.Rev - 1
		c := orderCols(o)
		tag, err := tx.Exec(ctx, `UPDATE orders SET status=$2, start_at=$3, degraded=$4, rev=$5, doc=$6, updated_at=now()
			WHERE order_id=$1 AND rev=$7`, c[0], c[6], c[8], c[9], c[10], c[11], old)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errCASConflict
		}
		return nil
	}
}

func insertOutbox(ctx context.Context, tx pgx.Tx, evs []Envelope) error {
	for _, e := range evs {
		body, _ := json.Marshal(e)
		nb := time.Now()
		if e.notBefore != nil {
			nb = *e.notBefore
		}
		if _, err := tx.Exec(ctx, `INSERT INTO outbox (event_id, subject, body, not_before) VALUES ($1,$2,$3,$4) ON CONFLICT (event_id) DO NOTHING`,
			e.EventID, e.Type, body, nb); err != nil {
			return err
		}
	}
	return nil
}

// vehicleLocUpdate 订单结束后车辆的最后位置与站点（附近车与扫码页用）。
type vehicleLocUpdate struct {
	VehicleID string
	Location  any
	StationID string
}

func updateVehicleLoc(ctx context.Context, tx pgx.Tx, u *vehicleLocUpdate) error {
	loc, _ := json.Marshal(u.Location)
	st, _ := json.Marshal(u.StationID)
	_, err := tx.Exec(ctx, `UPDATE snap SET doc = doc || jsonb_build_object('location', $2::jsonb, 'stationId', $3::jsonb), rev = nextval('snap_rev')
		WHERE kind='vehicle' AND id=$1`, u.VehicleID, loc, st)
	return err
}

// nextOrderSeq 订单号日序号。
func (st *Store) nextOrderSeq(ctx context.Context, day string) (int, error) {
	var n int
	err := st.pool.QueryRow(ctx, `INSERT INTO order_day_seq(day, seq) VALUES ($1, 1)
		ON CONFLICT (day) DO UPDATE SET seq = order_day_seq.seq + 1 RETURNING seq`, day).Scan(&n)
	return n, err
}

// upsertSnap 写快照（整条覆盖）。
func upsertSnap(ctx context.Context, tx pgx.Tx, kind, id string, doc any) error {
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO snap(kind,id,doc) VALUES ($1,$2,$3)
		ON CONFLICT (kind,id) DO UPDATE SET doc=EXCLUDED.doc, rev=nextval('snap_rev')`, kind, id, b)
	return err
}

// mutateSnap 读出快照（加锁）、修改、写回。不存在时从零值开始。
func mutateSnap[T any](ctx context.Context, tx pgx.Tx, kind, id string, fn func(*T)) (*T, error) {
	var v T
	var b []byte
	err := tx.QueryRow(ctx, `SELECT doc FROM snap WHERE kind=$1 AND id=$2 FOR UPDATE`, kind, id).Scan(&b)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &v); err != nil {
			return nil, err
		}
	}
	fn(&v)
	if err := upsertSnap(ctx, tx, kind, id, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// snapRow 快照一行。
type snapRow struct {
	Kind string
	ID   string
	Doc  []byte
	Rev  int64
}

func (st *Store) loadSnap(ctx context.Context, sinceRev int64) ([]snapRow, error) {
	rows, err := st.pool.Query(ctx, `SELECT kind, id, doc, rev FROM snap WHERE rev > $1 ORDER BY rev`, sinceRev)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []snapRow
	for rows.Next() {
		var r snapRow
		if err := rows.Scan(&r.Kind, &r.ID, &r.Doc, &r.Rev); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// withAdvisoryLock 多实例只跑一份的定时任务锁。
func (st *Store) withAdvisoryLock(ctx context.Context, key int64, fn func(ctx context.Context) error) (bool, error) {
	conn, err := st.pool.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&got); err != nil {
		return false, err
	}
	if !got {
		return false, nil
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, key)
	return true, fn(ctx)
}

// in 构造 IN 列表占位。
func inList(start int, n int) string {
	ps := make([]string, n)
	for i := range ps {
		ps[i] = fmt.Sprintf("$%d", start+i)
	}
	return strings.Join(ps, ",")
}
