---
title: gt7-telemetry-collector
layout: hextra-home
---

{{< hextra/hero-headline >}}
gt7-telemetry-collector
{{< /hextra/hero-headline >}}

{{< hextra/hero-subtitle >}}
From PS5 UDP packets to canonical telemetry frames, ready for RTPS.
{{< /hextra/hero-subtitle >}}

{{< hextra/hero-button text="Get started" link="docs/install/" >}}
{{< hextra/hero-button text="Documentation" link="docs/" >}}
{{< hextra/hero-button text="GitHub" link="https://github.com/arush-sal/gt7-telemetry-collector" >}}

A standalone Go collector and reusable library for Gran Turismo 7 telemetry.
Receive encrypted packets from a PS5, decrypt and deduplicate them, and stream
canonical protobuf frames over gRPC, a Unix stream socket, or HTTP(S).

[Explore the Go API](https://pkg.go.dev/github.com/arush-sal/gt7-telemetry-collector)
