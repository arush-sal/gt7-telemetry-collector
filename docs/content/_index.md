---
title: gt7-telemetry-collector
layout: hextra-home
---

<div class="hx:mt-6 hx:mb-6">
{{< hextra/hero-badge link="https://github.com/arush-sal/gt7-telemetry-collector/releases/latest" >}}
  <span>Latest release</span>
  {{< icon name="arrow-circle-right" attributes="height=14" >}}
{{< /hextra/hero-badge >}}
</div>

<div class="hx:mb-6">
{{< hextra/hero-headline >}}
  Gran Turismo 7 telemetry,&nbsp;<br class="hx:sm:block hx:hidden" />decoded and streamed
{{< /hextra/hero-headline >}}
</div>

<div class="hx:mb-12">
{{< hextra/hero-subtitle >}}
  A Go collector and library that turns encrypted PS5 UDP packets&nbsp;<br class="hx:sm:block hx:hidden" />into canonical protobuf frames over gRPC, Unix sockets or HTTP(S).
{{< /hextra/hero-subtitle >}}
</div>

<div class="hx:mb-6">
{{< hextra/hero-button text="Get started" link="docs/install/" style="margin: 0 .5rem .5rem 0" >}}
{{< hextra/hero-button text="Read the docs" link="docs/" style="margin: 0 .5rem .5rem 0; background: transparent; color: inherit; box-shadow: inset 0 0 0 1px rgba(127,127,127,.45)" >}}
{{< hextra/hero-button text="Go API" link="https://pkg.go.dev/github.com/arush-sal/gt7-telemetry-collector" style="margin: 0 .5rem .5rem 0; background: transparent; color: inherit; box-shadow: inset 0 0 0 1px rgba(127,127,127,.45)" >}}
</div>

<div class="hx:mt-12"></div>

{{< hextra/feature-grid >}}
  {{< hextra/feature-card
    title="PS5 to protobuf"
    subtitle="Heartbeats the console, decrypts Salsa20 packets, drops duplicates and maps every frame to one game-agnostic canonical schema."
    icon="lightning-bolt"
    link="docs/"
  >}}
  {{< hextra/feature-card
    title="Pick your transport"
    subtitle="Stream frames over gRPC, a Unix socket (length-prefixed protobuf) or HTTP(S) NDJSON, chosen by one URL."
    icon="switch-horizontal"
    link="docs/transports/"
  >}}
  {{< hextra/feature-card
    title="Raw pcap dumps"
    subtitle="Keep the exact encrypted wire bytes as nanosecond pcap. Replay them later or open them in Wireshark."
    icon="film"
    link="docs/raw-dumps/"
  >}}
  {{< hextra/feature-card
    title="Use it as a library"
    subtitle="Receiver, pipeline, decoder, replay and publishers are plain Go packages you can embed in your own tools."
    icon="code"
    link="docs/library/"
  >}}
  {{< hextra/feature-card
    title="Optional token auth"
    subtitle="Bearer tokens on gRPC and HTTP(S), plaintext warnings, and owner-only Unix sockets."
    icon="key"
    link="docs/security/"
  >}}
  {{< hextra/feature-card
    title="Signed releases"
    subtitle="Every archive, checksum and SBOM is cosign-signed, with GitHub build provenance you can verify."
    icon="shield-check"
    link="docs/install/#verify-a-release"
  >}}
{{< /hextra/feature-grid >}}
