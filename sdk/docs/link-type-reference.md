# LinkType Reference

## Purpose

`link_type` describes the capture representation. It determines how `DecodeRequest.payload` should be interpreted.

A plugin MUST NOT assume payload is already L7.

## Examples

### DLT_NULL

Example:

```
02 00 00 00 45 00 ...
```

The payload representation contains:

```
DLT_NULL + IP + transport + application
```

### Ethernet

Example:

```
ff ff ff ff ff ff ...
08 00
45 ...
```

Representation:

```
Ethernet
IPv4
TCP/UDP
Application
```

### RawIP

Example:

```
45 00 ...
```

Representation:

```
IP
Transport
Application
```

### ProxyPayload

Representation:

```
Application bytes only
```

## Important rule

```
link_type != protocol_hint
```

For example:

```
protocol_hint=tcp
```

does NOT mean:

```
HTTP
WebSocket
Game protocol
```

Plugins must inspect actual bytes and framing.