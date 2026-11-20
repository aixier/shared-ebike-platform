package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	natsserver "github.com/nats-io/nats-server/v2/server"
)

// 本包的测试共用一个真库（embedded-postgres，端口 = 18307 + 1000）与一个进程内 nats-server。

const testPGPort = 19307

var (
	testPool *pgxpool.Pool
	testNats *natsserver.Server

	testDSN     string
	testTmpRoot string
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	root, err := filepath.Abs(filepath.Join("..", "..", ".tmp"))
	if err != nil {
		fmt.Println(err)
		return 1
	}
	testTmpRoot = root
	_ = os.RemoveAll(filepath.Join(root, "pg-data"))
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).
		Port(testPGPort).
		Database("bike_rule").
		RuntimePath(filepath.Join(root, "pg-runtime")).
		DataPath(filepath.Join(root, "pg-data")).
		Locale("C").
		Encoding("SQL_ASCII").
		StartTimeout(60 * time.Second).
		Logger(io.Discard))
	if err := pg.Start(); err != nil {
		fmt.Println("启动 embedded-postgres 失败：", err)
		return 1
	}
	defer func() { _ = pg.Stop() }()

	natsDir, _ := os.MkdirTemp(root, "nats-")
	defer os.RemoveAll(natsDir)
	ns, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: natsDir, NoLog: true, NoSigs: true})
	if err != nil {
		fmt.Println(err)
		return 1
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		fmt.Println("nats-server 未就绪")
		return 1
	}
	defer ns.Shutdown()
	testNats = ns

	testDSN = fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/bike_rule?sslmode=disable", testPGPort)
	ctx := context.Background()
	if err := Migrate(ctx, testDSN); err != nil {
		fmt.Println("迁移失败：", err)
		return 1
	}
	testPool, err = pgxpool.New(ctx, testDSN)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	defer testPool.Close()

	return m.Run()
}

// 测试用管理令牌密钥对：包变量初始化时就要用，所以在这里生成而不是在 TestMain 里。
var testPub, testPriv, _ = ed25519.GenerateKey(rand.Reader)
