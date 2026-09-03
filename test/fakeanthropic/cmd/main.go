package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/CherryHQ/stella/test/fakeanthropic"
)

func main() {
	port := flag.Int("port", 25901, "listen port")
	chunks := flag.Int("chunks", 1500, "stream chunk count")
	interval := flag.Int("interval-ms", 10, "stream interval in milliseconds")
	flag.Parse()
	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	log.Printf("fakeanthropic listening on http://%s", addr)
	log.Printf("stream knobs: chunks=%d interval=%dms", *chunks, *interval)
	log.Fatal(http.ListenAndServe(addr, fakeanthropic.MessageHandlerWithOptions(fakeanthropic.MessageHandlerOptions{StreamChunks: *chunks, StreamIntervalMS: *interval})))
}
