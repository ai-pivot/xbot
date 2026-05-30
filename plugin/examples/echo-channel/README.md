# Echo Channel Plugin

An example channel plugin demonstrating the new gRPC transport protocol.

## Architecture

```
┌─────────────────────────────┐
│ HTTP Client (curl/browser)   │
│ POST /message -d 'hello'     │
└──────────────┬──────────────┘
               │
┌──────────────▼──────────────┐
│ Echo Plugin (Python)         │
│                              │
│  HTTP server ← HTTP request  │
│  stdin ← WSMessage events    │
│  stdout → RPC requests       │
└──────────────┬──────────────┘
               │ stdin/stdout
┌──────────────▼──────────────┐
│ xbot (GrpcPluginTransport)   │
│                              │
│  readLoop ← plugin stdout    │
│  PushEvent → plugin stdin    │
│  RPCTable.Dispatch()         │
└──────────────────────────────┘
```

## Protocol

The plugin communicates with xbot using the same JSON-RPC protocol as WebSocket clients:

**Plugin → xbot (RPC request):**
```json
{"id":"plugin-1","method":"send_inbound","params":{"channel":"echo","chat_id":"http_default","content":"hello","sender_id":"http_user","sender_name":"HTTP User","chat_type":"p2p"}}
```

**xbot → Plugin (event push):**
```json
{"type":"progress_structured","chat_id":"http_default","progress":{"phase":"thinking","message":"Processing..."}}
```

```json
{"type":"stream_content","chat_id":"http_default","content":"Hello! "}
```

```json
{"type":"text","chat_id":"http_default","content":"Hello! How can I help you?"}
```

## Setup

1. Install the plugin:
   ```bash
   cp -r plugin/examples/echo-channel/ ~/.xbot/plugins/echo-channel/
   ```

2. Add to `config.json`:
   ```json
   {
     "plugins": {
       "enabled": true
     },
     "channels": {
       "echo": {
         "enabled": "true",
         "port": "9876"
       }
     }
   }
   ```

3. Restart xbot

4. Test:
   ```bash
   curl -X POST http://localhost:9876/message -d 'Hello xbot!'
   curl http://localhost:9876/message?q=hello
   curl http://localhost:9876/health
   ```

## Activation Flow

1. xbot activates the plugin via the old plugin protocol (activate → response with channel_provider)
2. xbot spawns a **separate** process for the channel (using the entry command from the manifest)
3. The new process communicates via bidirectional JSON-RPC over stdin/stdout
4. xbot pushes a `channel_config` event with the channel configuration
5. The plugin starts its HTTP server and begins forwarding messages
