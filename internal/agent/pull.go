package agent

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

const (
	pullDialTimeout = 20 * time.Second
	pullPingEvery   = 20 * time.Second
	pullIdleAfter   = 70 * time.Second
	pullWaitMin     = time.Second
	pullWaitMax     = 30 * time.Second
	// A connection that lasted this long was a good one, so the next dial does not wait out the failures before it.
	pullStableAfter = 30 * time.Second
	// The same complaint is logged this often, while the master stays out of reach.
	pullComplainEvery = 10 * time.Minute
)

// pullLoop is the agent in pull mode: it dials the master, answers what the master sends over that connection,
// and dials again when it ends. The fields are what a test replaces.
type pullLoop struct {
	bundle *agentproto.Bundle
	server func() *http.Server // a server of its own for each connection, which spends it
	dial   func(ctx context.Context, b *agentproto.Bundle) (*agentproto.Conn, error)
	sleep  func(ctx context.Context, d time.Duration)
	now    func() time.Time
}

// ServePull answers the master over connections that the agent opens itself, until ctx ends. The core and the
// helper programs run whether or not the master is out of reach.
func (s *Server) ServePull(ctx context.Context, b *agentproto.Bundle) {
	(&pullLoop{bundle: b, server: s.newHTTPServer, dial: dialMaster, sleep: sleepCtx, now: time.Now}).run(ctx)
}

func (p *pullLoop) run(ctx context.Context) {
	var complaint string
	var complainedAt time.Time
	wait := pullWaitMin
	for ctx.Err() == nil {
		conn, err := p.dial(ctx, p.bundle)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if text := err.Error(); text != complaint || p.now().Sub(complainedAt) >= pullComplainEvery {
				logger.Warningf("agent: cannot reach the master, trying again in %s: %v", wait, err)
				complaint, complainedAt = text, p.now()
			}
			p.sleep(ctx, jittered(wait))
			wait = min(wait*2, pullWaitMax)
			continue
		}
		complaint = ""
		logger.Info("agent: connected to the master")
		began := p.now()
		agentproto.ServeConn(ctx, conn, p.server())
		if ctx.Err() != nil {
			return
		}
		if p.now().Sub(began) >= pullStableAfter {
			wait = pullWaitMin
		}
		logger.Warningf("agent: the connection to the master ended, dialing again in %s", wait)
		p.sleep(ctx, jittered(wait))
		wait = min(wait*2, pullWaitMax)
	}
}

// dialMaster opens the WebSocket that all of pull mode runs over, with the secret as its bearer.
func dialMaster(ctx context.Context, b *agentproto.Bundle) (*agentproto.Conn, error) {
	target, err := agentproto.ConnectURL(b.Master, b.Node)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := masterTLS(b)
	if err != nil {
		return nil, err
	}
	dialer := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: pullDialTimeout,
		TLSClientConfig:  tlsConfig,
		Subprotocols:     []string{agentproto.ConnectProtocol},
	}
	ws, resp, err := dialer.DialContext(ctx, target, http.Header{"Authorization": {"Bearer " + b.Secret}})
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("the master answered HTTP %d: the node, its mode or the secret is not what the master holds", resp.StatusCode)
		}
		return nil, err
	}
	if ws.Subprotocol() != agentproto.ConnectProtocol {
		_ = ws.Close()
		return nil, errors.New("the address of the master is not one that takes an agent")
	}
	conn := agentproto.NewConn(ws, nil)
	conn.KeepAlive(pullPingEvery, pullIdleAfter)
	return conn, nil
}

// masterTLS trusts the master by the chain of its certificate, or by the pin of the bundle when it has one,
// which is how a panel with a certificate of its own is trusted.
func masterTLS(b *agentproto.Bundle) (*tls.Config, error) {
	if b.Pin == "" {
		return &tls.Config{MinVersion: tls.VersionTLS12}, nil
	}
	pin, err := agentproto.ParsePin(b.Pin)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // lgtm[go/disabled-certificate-check]
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("the master presented no certificate")
			}
			sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(sum[:], pin) != 1 {
				return errors.New("the certificate of the master does not match the pin of the bundle")
			}
			return nil
		},
	}, nil
}

// jittered spreads the agents of one master over a window, so that the ones that lost it together do not all
// come back at the same moment.
func jittered(d time.Duration) time.Duration {
	return d + rand.N(d/5+1)
}

func sleepCtx(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
