//    magneticos searches torrents in a magnetico database, queries the current
//    number of seeders and lets the user select the one they want
//
//    Copyright (C) 2021 Matthieu Rakotojaona
//
//    This program is free software: you can redistribute it and/or modify
//    it under the terms of the GNU General Public License as published by
//    the Free Software Foundation, either version 3 of the License, or
//    (at your option) any later version.
//
//    This program is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//    GNU General Public License for more details.
//
//    You should have received a copy of the GNU General Public License
//    along with this program.  If not, see <https://www.gnu.org/licenses/>

package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"net/http"
	"net/url"
	"context"
	"time"
	"net"
	"math/rand"
	"encoding/binary"
	"encoding/hex"
	"bytes"
	"io"
	"errors"

	"golang.org/x/sync/errgroup"
	"github.com/anacrolix/torrent/bencode"
)

var timeout time.Duration

var client http.Client = http.Client(*http.DefaultClient)

var trackers []string = []string {

	"udp://tracker.opentrackr.org:1337/announce",
	"http://tracker.internetwarriors.net:1337/announce",
	"udp://exodus.desync.com:6969/announce",
	"udp://tracker.cyberia.is:6969/announce",
	"udp://explodie.org:6969/announce",
	"udp://opentracker.i2p.rocks:6969/announce",
	"udp://47.ip-51-68-199.eu:6969/announce",
	"http://open.acgnxtracker.com:80/announce",
	"udp://tracker.tiny-vps.com:6969/announce",
	"udp://www.torrent.eu.org:451/announce",
	"udp://tracker.torrent.eu.org:451/announce",
	"udp://tracker.ds.is:6969/announce",
	"udp://retracker.lanta-net.ru:2710/announce",
	"udp://open.stealth.si:80/announce",
	"udp://ipv4.tracker.harry.lu:80/announce",
	"udp://tracker.dler.org:6969/announce",
	"http://rt.tace.ru:80/announce",
	"udp://cdn-2.gamecoast.org:6969/announce",
	"udp://cdn-1.gamecoast.org:6969/announce",
	"udp://valakas.rollo.dnsabr.com:2710/announce",
}

func scrape(ctx context.Context, infohashes []string) (maxSeeders []int) {
	timeout, _ = time.ParseDuration("1s")
	client.Timeout = timeout

	g, ctx := errgroup.WithContext(ctx)
	seedersByTracker := make([][]int, len(trackers))
	for i, tracker := range trackers {
		i, tracker := i, tracker
		g.Go(func() error {
			if strings.HasPrefix(tracker, "http") {
				seedersByTracker[i] = scrapeHttp(ctx, tracker, infohashes)
			} else if strings.HasPrefix(tracker, "udp") {
				seedersByTracker[i] = scrapeUdp(ctx, tracker, infohashes)
			}
			return nil
		})
	}

	g.Wait()
	
	maxSeeders = make([]int, len(infohashes))
	for _, seeders := range seedersByTracker {
		for i := range infohashes {
			if maxSeeders[i] < seeders[i] {
				maxSeeders[i] = seeders[i]
			}
		}
	}

	return
}

func scrapeUdp(ctx context.Context, tracker string, infohashes []string) (seeders []int) {
	seeders = make([]int, 0)

	for len(infohashes) > 0 {
		sliceMax := 74
		if len(infohashes) < sliceMax {
			sliceMax = len(infohashes)
		}
		slice := infohashes[:sliceMax]
		seedersSlice := doScrapeUdp(ctx, tracker, slice)
		seeders = append(seeders, seedersSlice...)
		infohashes = infohashes[sliceMax:]
	}

	return
}

func doScrapeUdp(ctx context.Context, tracker string, infohashes []string) (seeders []int) {
	seeders = make([]int, len(infohashes))

	parsed, err := url.Parse(tracker)
	if err != nil {
		return
	}

	raddr, err := net.ResolveUDPAddr("udp", parsed.Host)
	if err != nil {
		log.Printf("KO dial %s\n", tracker)
		return
	}

	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		log.Printf("KO dial %s\n", tracker)
		return
	}

	// 1. Connection
	var connectionRequestBuf bytes.Buffer
	err = binary.Write(&connectionRequestBuf, binary.BigEndian, uint64(0x41727101980))
	if err != nil {
		return
	}
	err = binary.Write(&connectionRequestBuf, binary.BigEndian, uint32(0))
	if err != nil {
		return
	}
	transactionId := rand.Uint32()
	err = binary.Write(&connectionRequestBuf, binary.BigEndian, transactionId)
	if err != nil {
		return
	}
	conn.SetDeadline(soon())
	_, err = io.Copy(conn, &connectionRequestBuf)
	if err != nil {
		log.Printf("KO connect out%s\n", tracker)
		return
	}

	var connectReply [16]byte
	ok := readUDPReply(conn, connectReply[:], transactionId, 0, len(connectReply), tracker)
	if !ok {
		return
	}

	// 2. scrape
	connectionIdBytes := connectReply[8:16]

	var scrapeRequestBuf bytes.Buffer
	_, err = scrapeRequestBuf.Write(connectionIdBytes)
	if err != nil {
		return
	}
	err = binary.Write(&scrapeRequestBuf, binary.BigEndian, uint32(2))
	if err != nil {
		return
	}
	transactionId = rand.Uint32()
	err = binary.Write(&scrapeRequestBuf, binary.BigEndian, transactionId)
	if err != nil {
		return
	}

	for _, infohash := range infohashes {
		ih, err := hex.DecodeString(infohash)
		if err != nil {
			return
		}
		_, err = scrapeRequestBuf.Write(ih)
		if err != nil {
			return
		}
	}

	conn.SetDeadline(soon())
	_, err = io.Copy(conn, &scrapeRequestBuf)
	if err != nil {
		log.Printf("KO connect out%s\n", tracker)
		return
	}

	scrapeReply := make([]byte, 8+12*len(infohashes))
	ok = readUDPReply(conn, scrapeReply, transactionId, 2, len(scrapeReply), tracker)
	if !ok {
		return
	}

	for i := range infohashes {
		seeders[i] = int(binary.BigEndian.Uint32(scrapeReply[8+12*i:8+12*i+4]))
	}

	return
}

func readUDPReply(conn net.Conn, reply []byte, transactionId uint32, expectedAction uint32, expectedLen int, tracker string) bool {
	conn.SetDeadline(soon())
	n, err := io.ReadAtLeast(conn, reply, 8)
	if err != nil {
		if err == io.ErrShortBuffer {
			log.Printf("Reply too short %s\n", tracker)
		} else if !errors.Is(err, os.ErrDeadlineExceeded) {
			log.Printf("KO connect in %s\n", tracker)
			log.Printf("\t%v\n", err)
		}
		return false
	}

	if binary.BigEndian.Uint32(reply[4:8]) != transactionId {
		log.Printf("Wrong transactionId %s\n", tracker)
		return false
	}

	action := binary.BigEndian.Uint32(reply[0:4])
	switch action {
	case expectedAction: 
		// connect
		if n != expectedLen {
			log.Printf("short read %s\n", tracker)
			return false
		}
	case 3: 
		// error
		log.Printf("Error %s\n", tracker)
		return false
	default:
		log.Printf("Unknown action %d %s\n", action, tracker)
		return false
	}

	return true
}

func scrapeHttp(ctx context.Context, tracker string, infohashes []string) (seeders []int) {
	seeders = make([]int, len(infohashes))

	base := strings.Replace(tracker, "announce", "scrape", -1)
	urlAll, err := url.Parse(base)
	if err != nil {
		log.Printf("Couldn't parse %s: %s\n", base, err)
		return
	}
	
	finalUrl := urlAll.String() + "?" 
	for _, infohash := range infohashes {
		finalUrl += "info_hash=" + infohash + "&"
	}

	req, err := http.NewRequestWithContext(ctx, "GET", finalUrl, nil)
	r, err := client.Do(req)
	if err != nil {
		if err.(*url.Error).Timeout() {
			// log.Println("timeout", base)
		}
		return
	} else if r.StatusCode != 200 {
		// log.Printf("%d %s\n", r.StatusCode, base)
		return
	}

	defer r.Body.Close()

	var v ScrapeResult
	err = bencode.NewDecoder(r.Body).Decode(&v)
	if err != nil {
		// log.Println(err, base)
		return
	}
	
	for i, infohash := range infohashes {
		file, exists := v.Files[fmt.Sprintf("%X", infohash)]
		if exists {
			seeders[i] = file.Complete
		}
	}
	return
}

func soon() time.Time {
	return time.Now().Add(timeout)
}

func magnetFrom(infohash string) string {
	link := fmt.Sprintf("magnet:?xt=urn:btih:%s", infohash)
	for _, tracker := range trackers {
		link += fmt.Sprintf("&tr=%s", url.QueryEscape(tracker))
	}
	return link
}

type ScrapeResult struct {
	Files map[string]FileStruct `bencode:"files"`
}

type FileStruct struct {
	Complete int `bencode:"complete"`
}
