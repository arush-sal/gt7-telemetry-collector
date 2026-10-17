// Package replay plays a recorded GT7 capture (.pcap or .pcapng) through the collector's receive
// path: each datagram addressed to the telemetry recv port is handed, at its original capture cadence
// (scaled by a speed factor), to the same handler the live UDP Receiver feeds (typically
// ingest.Pipeline.Handle). Downstream results are therefore identical to the original live run.
package replay
