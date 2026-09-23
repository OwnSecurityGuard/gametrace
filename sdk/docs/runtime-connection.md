# Runtime Connection

> **Read this first.** The platform runs plugins in **tunnel mode only** (`GT_TUNNEL=1`, injected
> by gt-agent and by Developer Plane `activate_plugin`). The plugin opens no listener and the
> host never dials back — registration, heartbeat and every decode frame share the single
> connection to `GT_REGISTRY_ADDR`. That one address is all a plugin needs.

## Overview

```
        register | heartbeat | Connect stream (decode frames)

Plugin <=================================================> Registry
        (one outbound connection; no inbound port needed)
```

## GT_REGISTRY_ADDR

The plugin's registration target. Registration, heartbeat and decode frames all travel over
this one connection:

```
Plugin -> Registry
```

Supported endpoint forms: TCP (`host:port`), Unix socket (`unix:` or a bare path) and Windows
named pipe (`npipe:`). For development and integration tests, prefer setting
`GT_REGISTRY_ADDR` explicitly.

## Removed: decoder endpoint variables

`GT_DECODER_ADDR` and `GT_DECODER_PUBLIC_ADDR` no longer exist. They only configured the
"Pipeline -> Decoder" dial-back hop, which was removed together with the non-tunnel path.
The SDK does not read them at all, so setting them has no effect.
