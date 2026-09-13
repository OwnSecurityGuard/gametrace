# Runtime Connection

## Overview

```
                 register

Plugin --------------------> Registry


                 connect

Pipeline ------------------> Decoder
```

## GT_REGISTRY_ADDR

Plugin registration target:

```
Plugin -> Registry
```

## GT_DECODER_ADDR

Decoder listener address:

```
Decoder server listen
```

Default `:0` (random TCP port). When unset or bound to a wildcard
(`0.0.0.0` / `[::]`), the SDK advertises a concrete host IP (first non-loopback
IPv4) instead of the wildcard, so a Dockerized pipeline can dial the plugin back.

## GT_DECODER_PUBLIC_ADDR

The address advertised to pipeline:

```
Pipeline should connect here
```

Always used verbatim when set (highest priority). `GT_DECODER_ADDR` controls
where the decoder binds; `GT_DECODER_PUBLIC_ADDR` controls the endpoint exposed
to the pipeline.

For the common "host in Docker, plugin on the host machine" setup, set it to an
address the container can reach — the host LAN IP, or `host.docker.internal` on
Docker Desktop: