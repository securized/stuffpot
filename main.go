package main

import (
	"bufio"
	"crypto/tls"
	"database/sql"
	"flag"
	"fmt"
	"github.com/elazarl/goproxy"
	"github.com/elazarl/goproxy/transport"
	_ "github.com/mattn/go-sqlite3"
	"log"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
)

type HttpLogger struct {
	db *sql.DB
}

func NewLogger(dbname string) (*HttpLogger, error) {
	db, _ := sql.Open("sqlite3", "./log.db")

	_, err := db.Exec(`create table if not exists requests (
      id INTEGER PRIMARY KEY AUTOINCREMENT,
      from_ip TEXT,
      method TEXT,
      host TEXT,
      url TEXT,
      headers TEXT,
      created_at INTEGER DEFAULT CURRENT_TIMESTAMP
    )`)

	orPanic(err)

	logger := &HttpLogger{db}

	return logger, nil
}

func (logger *HttpLogger) LogReq(req *http.Request, ctx *goproxy.ProxyCtx) {
	var headersCol []string

	for name, headers := range req.Header {
		name = strings.ToLower(name)
		for _, h := range headers {
			headersCol = append(headersCol, fmt.Sprintf("%v: %v", name, h))
		}
	}

	tx, _ := logger.db.Begin()
	stmt, _ := tx.Prepare("insert into requests (from_ip, method, host, url, headers) values (?,?,?,?,?)")
	_, err := stmt.Exec(strings.Split(req.RemoteAddr, ":")[0], req.Method, req.Host, req.URL.String(), strings.Join(headersCol, "\r\n"))

	if err != nil {
		ctx.Logf("Failed to write request to db, error %v", err)
		return
	}

	err3 := tx.Commit()

	if err3 != nil {
		ctx.Logf("Failed to commit request to db, error %v", err)
	}
}

type stoppableListener struct {
	net.Listener
	sync.WaitGroup
}

type stoppableConn struct {
	net.Conn
	wg *sync.WaitGroup
}

func newStoppableListener(l net.Listener) *stoppableListener {
	return &stoppableListener{l, sync.WaitGroup{}}
}

func (sl *stoppableListener) Accept() (net.Conn, error) {
	c, err := sl.Listener.Accept()
	if err != nil {
		return c, err
	}
	sl.Add(1)
	return &stoppableConn{c, &sl.WaitGroup}, nil
}

func (sc *stoppableConn) Close() error {
	sc.wg.Done()
	return sc.Conn.Close()
}

func orPanic(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	proxy := goproxy.NewProxyHttpServer()

	verbose := flag.Bool("v", false, "Verbose log to stdout")
	addr := flag.String("addr", ":8080", "Listen Port")
	flag.Parse()
	proxy.Verbose = *verbose

	logger, _ := NewLogger("db")

	tr := transport.Transport{
		Proxy: transport.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{
			// Ignore cert errors
			InsecureSkipVerify: true,
		},
	}

	proxy.OnRequest(goproxy.ReqHostMatches(regexp.MustCompile("^.*$"))).HandleConnectFunc(func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
		return goproxy.MitmConnect, host
	})
	proxy.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		ctx.RoundTripper = goproxy.RoundTripperFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (resp *http.Response, err error) {
			ctx.UserData, resp, err = tr.DetailedRoundTrip(req)
			return
		})
		logger.LogReq(req, ctx)
		return req, nil
	})
	proxy.OnRequest(goproxy.ReqHostMatches(regexp.MustCompile("^.*:80$"))).
		// Deal with tunnel proxy connect requests
		HijackConnect(func(req *http.Request, client net.Conn, ctx *goproxy.ProxyCtx) {
			defer func() {
				if e := recover(); e != nil {
					ctx.Logf("error connecting to remote: %v", e)
					client.Write([]byte("HTTP/1.1 500 Cannot reach remote\r\n\r\n"))
				}
				client.Close()
			}()
			clientBuf := bufio.NewReadWriter(bufio.NewReader(client), bufio.NewWriter(client))
			remote, err := net.Dial("tcp", req.URL.Host)
			orPanic(err)
			client.Write([]byte("HTTP/1.1 200 Ok\r\n\r\n"))
			remoteBuf := bufio.NewReadWriter(bufio.NewReader(remote), bufio.NewWriter(remote))
			for {
				req, err := http.ReadRequest(clientBuf.Reader)
				orPanic(err)
				orPanic(req.Write(remoteBuf))
				orPanic(remoteBuf.Flush())
				resp, err := http.ReadResponse(remoteBuf.Reader, req)
				orPanic(err)
				orPanic(resp.Write(clientBuf.Writer))
				orPanic(clientBuf.Flush())
			}
		})

	log.Println("Starting Proxy")

	log.Fatal(http.ListenAndServe(*addr, proxy))
}
