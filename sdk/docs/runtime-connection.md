# Runtime Connection

> **Read this first.** The platform runs plugins in **tunnel mode** (`GT_TUNNEL=1`, injected by
> gt-agent and by Developer Plane `activate_plugin`). In tunnel mode the plugin opens no
> listener and the host never dials back — registration, heartbeat and every decode frame share
> the single connection to `GT_REGISTRY_ADDR`. `GT_DECODER_ADDR` and
> `GT_DECODER_PUBLIC_ADDR` are **not used** in that mode; the "Pipeline -> Decoder" hop below
> only exists in the legacy standard (non-tunnel) fallback path.

## Overview

```
        tunnel mode (what the platform uses)

        register | heartbeat | Connect stream (decode frames)

Plugin <=================================================> Registry
        (one outbound connection; no inbound port needed)


        standard mode (legacy fallback)

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

## GT_DECODER_ADDR  (standard / non-tunnel mode only)

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