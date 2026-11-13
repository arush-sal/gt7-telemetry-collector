---
title: Raw dumps
weight: 7
---

Set `--rawdump-dir` to open one **`raw-<unix>.pcap`** file per run, where
`<unix>` is the startup Unix timestamp in seconds. The directory is created if
needed (mode 0755; the file is 0644, truncated if it exists).

The format is classic pcap with nanosecond timestamps and Ethernet link type, so
dumps open in Wireshark and tcpdump and replay through the
[`replay`]({{< relref "library#replay-a-capture" >}}) package and RTPS `--replay-file`. The
still-encrypted UDP wire bytes are preserved.

## Synthetic headers

Each payload has synthetic Ethernet/IPv4/UDP headers: zero MAC addresses, IPv4
TTL 64 with valid checksums, source PS5 IP and the configured console send port,
and destination receiver IP and receive port. An unknown local IP is recorded as
`0.0.0.0`. These headers describe the configured endpoints, including for foreign
packets; they are synthesized, not captured from the network. IPv4 endpoints are
required when raw dumping is enabled. Payloads over `rawsink.MaxPayload` (65507
bytes) cannot be represented and are rejected with `rawsink.ErrPayloadTooLarge`.

## Why store encrypted bytes?

- **Lossless ground truth:** retained payloads preserve the original wire bytes,
  including data a decoder may not understand yet.
- **Rejected packets remain useful:** the raw tee runs before validation, so
  foreign, malformed and truncated packets can be inspected too.
- **Encryption provides no secrecy here:** the fixed GT7 Salsa20 key is public
  and present in the protocol implementation. Storing ciphertext preserves the
  input; it does not hide data from someone who has the file.
- **Exercise the decrypt path:** offline tools can feed retained bytes through
  the same decryption and decoding functions used during live collection.

## Limits

Raw dumping is buffered and best effort. Overflow increments `raw_dropped`; disk
write errors are logged. The receiver uses a 4096-byte datagram buffer, so larger
foreign datagrams may already be truncated by the read. Two starts in the same
directory within one second can reuse and truncate the same filename.

## Replaying

```go
source, err := replay.Open("rawdump/raw-1700000000.pcap")
```

`replay.Open` selects the reader by the file's magic bytes, not its extension,
so a pcapng saved under a `.pcap` name still opens. See
[Library]({{< relref "library#replay-a-capture" >}}) for a complete example. The collector
command has no replay flag.
