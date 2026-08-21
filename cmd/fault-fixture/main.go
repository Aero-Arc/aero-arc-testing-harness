package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/Aero-Arc/aero-arc-test-harness/internal/fixture"
)

func main() {
	address := flag.String("addr", ":8081", "HTTP listen address")
	peerURL := flag.String("peer-base-url", "http://fixture:8081", "USS URL advertised for fixture-owned intents")
	subscriberURL := flag.String("subscriber-url", "", "optional peer subscriber URL returned by DSS mutations")
	flag.Parse()

	server := &http.Server{
		Addr:              *address,
		Handler:           fixture.New(fixture.Config{PeerBaseURL: *peerURL, SubscriberURL: *subscriberURL}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	log.Printf("fault fixture listening on %s", *address)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
