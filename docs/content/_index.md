---
title: gt7-telemetry-collector
layout: hextra-home
---

<div class="gt7-hero hx:mt-12">
{{< hextra/hero-container image="images/hero.svg" imageTitle="Telemetry dashboard" imageWidth="440" imageHeight="356" cols="2" >}}

<div class="hx:mb-6">
{{< hextra/hero-badge link="https://github.com/arush-sal/gt7-telemetry-collector/releases/latest" >}}
  <span>Signed releases on GitHub</span>
  {{< icon name="arrow-circle-right" attributes="height=14" >}}
{{< /hextra/hero-badge >}}
</div>

<div class="hx:mb-6">
{{< hextra/hero-headline >}}
  Gran Turismo 7 telemetry, decoded and streamed
{{< /hextra/hero-headline >}}
</div>

<div class="hx:mb-12">
{{< hextra/hero-subtitle >}}
  A Go collector and library that turns encrypted PS5 UDP packets into canonical protobuf frames, streamed over gRPC, Unix sockets or HTTP(S).
{{< /hextra/hero-subtitle >}}
</div>

<div class="hx:mb-6">
{{< hextra/hero-button text="Get started" link="docs/install/" style="margin: 0 .5rem .5rem 0" >}}
{{< hextra/hero-button text="Documentation" link="docs/" style="margin: 0 .5rem .5rem 0; background: transparent; color: inherit; box-shadow: inset 0 0 0 1px rgba(127,127,127,.45)" >}}
</div>

{{< /hextra/hero-container >}}
</div>

<div class="gt7-section not-prose hx:mt-16 hx:mb-16">
<h2 class="gt7-section-title">One binary, three transports</h2>
<p class="gt7-section-sub">Point it at your console and your consumer. Everything else is a flag.</p>
{{< terminal >}}
<span class="p">$</span> go install github.com/arush-sal/gt7-telemetry-collector/cmd/gt7-collector@latest
<span class="p">$</span> export INGEST_TOKEN=change-me
<span class="p">$</span> gt7-collector --ps5=192.168.1.50 --rtps=https://rtps.lan:8443/v1/ingest --rawdump-dir=./rawdump

<span class="p">$</span> gt7-collector --help
Usage of gt7-collector:
      --heartbeat duration     heartbeat interval <span class="c">(default 1s)</span>
      --log-level string       log level: debug|info|warn|error <span class="c">(default "info")</span>
      --ps5 string             PS5 IP address <span class="c">(required)</span>
      --pub-buffer int         outbound canonical-frame buffer depth <span class="c">(default 1024)</span>
      --raw-buffer int         raw packet buffer depth <span class="c">(default 1024)</span>
      --rawdump-dir string     raw dump directory (empty disables dumping)
      --recv-port int          UDP receive port <span class="c">(default 33740)</span>
      --rtps string            RTPS target: host:port, grpc://, unix://, http(s):// <span class="c">(default "localhost:50051")</span>
      --rtps-token string      ingest bearer token (empty uses INGEST_TOKEN)
      --send-port int          UDP heartbeat destination port <span class="c">(default 33739)</span>
      --version                print version information and exit
{{< /terminal >}}
</div>

<div class="gt7-section not-prose hx:mb-8">
<h2 class="gt7-section-title">Everything between the console and your data</h2>
<p class="gt7-section-sub">Built for sim-racing tools, dashboards and race engineers that want clean, typed telemetry.</p>
</div>

{{< hextra/feature-grid >}}
  {{< hextra/feature-card
    title="PS5 to protobuf"
    subtitle="Heartbeats the console, decrypts Salsa20 packets, drops duplicates and maps every frame to one game-agnostic canonical schema."
    icon="lightning-bolt"
    link="docs/"
    style="background: radial-gradient(ellipse at 50% 80%,rgba(255,90,54,0.15),hsla(0,0%,100%,0));"
  >}}
  {{< hextra/feature-card
    title="Pick your transport"
    subtitle="Stream frames over gRPC, a Unix socket (length-prefixed protobuf) or HTTP(S) NDJSON, chosen by one URL."
    icon="switch-horizontal"
    link="docs/transports/"
    style="background: radial-gradient(ellipse at 50% 80%,rgba(255,170,60,0.15),hsla(0,0%,100%,0));"
  >}}
  {{< hextra/feature-card
    title="Raw pcap dumps"
    subtitle="Keep the exact encrypted wire bytes as nanosecond pcap. Replay them later or open them in Wireshark."
    icon="film"
    link="docs/raw-dumps/"
    style="background: radial-gradient(ellipse at 50% 80%,rgba(80,160,255,0.15),hsla(0,0%,100%,0));"
  >}}
  {{< hextra/feature-card
    title="Use it as a library"
    subtitle="Receiver, pipeline, decoder, replay and publishers are plain Go packages you can embed in your own tools."
    icon="code"
    link="docs/library/"
    style="background: radial-gradient(ellipse at 50% 80%,rgba(40,200,120,0.15),hsla(0,0%,100%,0));"
  >}}
  {{< hextra/feature-card
    title="Optional token auth"
    subtitle="Bearer tokens on gRPC and HTTP(S), plaintext warnings, and owner-only Unix sockets."
    icon="key"
    link="docs/security/"
    style="background: radial-gradient(ellipse at 50% 80%,rgba(170,90,255,0.15),hsla(0,0%,100%,0));"
  >}}
  {{< hextra/feature-card
    title="Signed releases"
    subtitle="Every archive, checksum and SBOM is cosign-signed, with GitHub build provenance you can verify."
    icon="shield-check"
    link="docs/install/#verify-a-release"
    style="background: radial-gradient(ellipse at 50% 80%,rgba(255,45,85,0.15),hsla(0,0%,100%,0));"
  >}}
{{< /hextra/feature-grid >}}

<div class="hx:mt-16"></div>
