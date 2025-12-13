# WebSocket API Documentation

## Endpoints

### WebSocket Connection
- **URL**: `ws://localhost:8080/ws`
- **Description**: Connect to WebSocket for realtime updates

### Donation Endpoints

#### 1. Trigger Donation (Show Overlay)
```bash
POST /api/donation/show
Content-Type: application/json

{
  "donorName": "Someguy",
  "amount": "Rp69.420",
  "message": "THIS IS A FAKE MESSAGE! HAVE A GOOD ONE",
  "mediaUrl": "https://example.com/video.mp4",
  "mediaType": "video"
}
```

#### 2. Trigger Donation Only
```bash
POST /api/donation/trigger
Content-Type: application/json

{
  "donorName": "Someguy",
  "amount": "Rp69.420",
  "message": "Optional message"
}
```

#### 3. Set Media URL
```bash
POST /api/donation/media
Content-Type: application/json

{
  "mediaUrl": "https://example.com/video.mp4",
  "mediaType": "video"  // or "image"
}
```

#### 4. Show/Hide Overlay
```bash
POST /api/donation/visibility
Content-Type: application/json

{
  "visible": true  // or false
}
```

## WebSocket Message Types

### Donation Message
```json
{
  "type": "donation",
  "donorName": "Someguy",
  "amount": "Rp69.420",
  "message": "Optional message",
  "visible": true
}
```

### Media Message
```json
{
  "type": "media",
  "mediaUrl": "https://example.com/video.mp4",
  "mediaType": "video",
  "visible": true
}
```

### Visibility Message
```json
{
  "type": "visibility",
  "visible": true
}
```

## Example Usage

### Trigger donation from Go code:
```go
// Using HTTP client
resp, err := http.Post(
    "http://localhost:8080/api/donation/show",
    "application/json",
    strings.NewReader(`{
        "donorName": "Someguy",
        "amount": "Rp69.420",
        "message": "Thank you!",
        "mediaUrl": "https://example.com/video.mp4",
        "mediaType": "video"
    }`),
)
```

### Using curl:
```bash
curl -X POST http://localhost:8080/api/donation/show \
  -H "Content-Type: application/json" \
  -d '{
    "donorName": "Someguy",
    "amount": "Rp69.420",
    "message": "Thank you!",
    "mediaUrl": "https://example.com/video.mp4",
    "mediaType": "video"
  }'
```

