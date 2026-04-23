package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

type workerContext struct {
	ctx    context.Context
	cancel func()
	client *http.Client
	baseURL string
	scheme  string
}

func (wctx *workerContext) reset(baseURL, scheme string) {
	wctx.baseURL = baseURL
	wctx.scheme = scheme

	wctx.client = &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
		Timeout: 5 * time.Second,
		// Removed: CheckBody (doesn't exist), Jar (unnecessary for spraying)
	}

	wctx.client.Transport = &http.Transport{
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		// Removed: DualStack (removed in Go 1.16), ForceAttemptHTTP2 kept below
		ForceAttemptHTTP2: false,
		DisableKeepAlives: false,

		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: false,
			MinVersion:         tls.VersionTLS12, // TLS 1.0 is broken; 1.2 is the safe floor
			CipherSuites: []uint16{
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
				tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
			},
			CurvePreferences: []tls.CurveID{
				tls.X25519,
				tls.CurveP384, // was tls.P384 — correct name
			},
			// Removed: HandshakeTimeout (not a tls.Config field — set on Transport above)
			ClientSessionCache: tls.NewLRUClientSessionCache(64),
			Renegotiation:      tls.RenegotiateOnceAsClient, // was RenegotiationOncePerConnection
			// Removed: InsecureSkipHeaderFields (doesn't exist)
		},
	}
}

func basicSpray(wg *sync.WaitGroup, channelToCommunicate chan string, taskToRun task, storeResult *int) {
	defer wg.Done()
	var internalCounter int64 = 0

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wctx := &workerContext{
		ctx:    ctx,
		cancel: cancel,
	}

	if len(taskToRun.targetsRaw) > 0 {
		firstTarget := parseTarget(taskToRun.targetsRaw[0])
		wctx.reset(firstTarget.host, firstTarget.scheme)
	}

	for _, taskTarget := range taskToRun.targetsRaw {
		temporaryTarget := parseTarget(taskTarget)

		if wctx.baseURL != temporaryTarget.host || wctx.scheme != temporaryTarget.scheme {
			wctx.reset(temporaryTarget.host, temporaryTarget.scheme)
		}

		baseURL := fmt.Sprintf("%s://%s:%d", wctx.scheme, temporaryTarget.host, taskToRun.target.port)

		for _, password := range taskToRun.passwords {
			for _, username := range taskToRun.usernames {
				// storeResult is a plain *int, so read it with a regular load (no atomic needed here)
				// If storeResult is shared across goroutines, switch it to *int64 and use atomic.LoadInt64
				if internalCounter < int64(*storeResult) {
					internalCounter++
					continue
				}

				headerValue := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))

				req, err := http.NewRequestWithContext(wctx.ctx, "GET", baseURL+"/"+taskToRun.target.url, nil)
				if err != nil {
					internalCounter++
					continue
				}
				req.Header.Set("Authorization", "Basic "+headerValue)

				res, err := wctx.client.Do(req)
				if err != nil {
					internalCounter++
					continue
				}

				bodyBytes, readErr := io.ReadAll(res.Body)
				res.Body.Close()

				if readErr != nil {
					internalCounter++
					continue
				}
				_ = bodyBytes

				switch {
				case res.StatusCode == 200 || (res.StatusCode >= 300 && res.StatusCode < 599):
					channelToCommunicate <- fmt.Sprintf("%s:%s:%s", temporaryTarget.host, username, password)
					fmt.Print("+")
				case res.StatusCode == 401:
					fmt.Print("-")
				}

				internalCounter++
			}
		}
	}
}