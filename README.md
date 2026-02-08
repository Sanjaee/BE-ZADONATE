# OBS Hit API

Go service untuk connect ke OBS Studio via WebSocket dan melakukan "hit" (ganti scene / play sound) melalui HTTP API.

## 📋 Prerequisites

- **Go** ≥ 1.23
- **OBS Studio** ≥ 28
- **Docker** (optional, untuk running dengan Docker)

## 🔧 Setup OBS Studio

1. Buka OBS Studio
2. **Tools** → **WebSocket Server Settings**
3. Enable **Enable WebSocket Server**
4. Set:
   - **Server Port**: `4455`
   - **Server Password**: `password123`
5. Buat Scene dan Media Source:
   - Scene: **Donasi** (atau nama lain sesuai kebutuhan)
   - Media Source: **DonasiSound** (untuk play sound)

## 🚀 Installation

### 1. Clone atau Download Project

```bash
cd be
```

### 2. Install Dependencies

```bash
go mod download
```

### 3. Run Application

```bash
go run .
```

Server akan berjalan di `http://localhost:5000`

## 🐳 Run dengan Docker

### Build & Run dengan Docker Compose

```bash
docker-compose up --build
```

### Run dengan Docker Manual

```bash
docker build -t obs-hit .
docker run -p 5000:5000 \
  -e OBS_HOST=host.docker.internal:4455 \
  -e OBS_PASSWORD=password123 \
  obs-hit
```

**Note**: 
- Windows/Mac: Gunakan `host.docker.internal:4455`
- Linux: Ganti dengan IP host machine atau gunakan `--network host`

## ⚙️ Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `5000` | Port untuk HTTP server |
| `GIN_MODE` | `release` | Gin framework mode (`debug` / `release`) |
| `OBS_HOST` | `localhost:4455` | OBS WebSocket host dan port |
| `OBS_PASSWORD` | `password123` | OBS WebSocket password |

## 📚 API Endpoints

### Health Check

```http
GET /health
```

**Response:**
```
OK
```

---

### Browser OBS API

#### Get All Scenes
```http
GET /api/obs/scenes
```

**Response:**
```json
{
  "scenes": [
    {
      "sceneIndex": 0,
      "sceneName": "Donasi"
    }
  ]
}
```

#### Get Current Scene
```http
GET /api/obs/scene/current
```

**Response:**
```json
{
  "sceneName": "Donasi"
}
```

#### Get All Inputs
```http
GET /api/obs/inputs
```

**Response:**
```json
{
  "inputs": [
    {
      "inputKind": "ffmpeg_source",
      "inputName": "DonasiSound",
      "unversionedInputKind": "ffmpeg_source"
    }
  ]
}
```

#### Get OBS Info
```http
GET /api/obs/info
```

**Response:**
```json
{
  "obsVersion": "30.0.0",
  "obsWebSocketVersion": "5.0.0",
  "platform": "windows"
}
```

---

### Hit Endpoints

#### Hit Scene (Ganti Scene)
```http
GET /hit/scene?scene=Donasi
```

**Query Parameters:**
- `scene` (optional): Nama scene, default: `Donasi`

**Response:**
```json
{
  "success": true,
  "message": "Scene switched",
  "sceneName": "Donasi"
}
```

#### Hit Sound (Play Sound)
```http
GET /hit/sound?input=DonasiSound
```

**Query Parameters:**
- `input` (optional): Nama input/media source, default: `DonasiSound`

**Response:**
```json
{
  "success": true,
  "message": "Sound played",
  "inputName": "DonasiSound"
}
```

---

### Test Endpoints

#### Test Hit Scene
```http
GET /api/test/scene?scene=Donasi
```

**Query Parameters:**
- `scene` (optional): Nama scene, default: `Donasi`

**Response:**
```json
{
  "success": true,
  "message": "Test scene hit successful",
  "sceneName": "Donasi"
}
```

#### Test Hit Sound
```http
GET /api/test/sound?input=DonasiSound
```

**Query Parameters:**
- `input` (optional): Nama input/media source, default: `DonasiSound`

**Response:**
```json
{
  "success": true,
  "message": "Test sound hit successful",
  "inputName": "DonasiSound"
}
```

---

## 🧪 Testing dengan cURL

### Health Check
```bash
curl http://localhost:5000/health
```

### Browser OBS API
```bash
# Get all scenes
curl http://localhost:5000/api/obs/scenes

# Get current scene
curl http://localhost:5000/api/obs/scene/current

# Get all inputs
curl http://localhost:5000/api/obs/inputs

# Get OBS info
curl http://localhost:5000/api/obs/info
```

### Hit Endpoints
```bash
# Hit scene (default)
curl http://localhost:5000/hit/scene

# Hit scene (custom)
curl http://localhost:5000/hit/scene?scene=Donasi

# Hit sound (default)
curl http://localhost:5000/hit/sound

# Hit sound (custom)
curl http://localhost:5000/hit/sound?input=DonasiSound
```

### Test Endpoints
```bash
# Test hit scene
curl http://localhost:5000/api/test/scene?scene=Donasi

# Test hit sound
curl http://localhost:5000/api/test/sound?input=DonasiSound
```

## 📦 Postman Collection

Import file `OBS_Hit_API.postman_collection.json` ke Postman untuk testing yang lebih mudah.

## 🔍 Troubleshooting

### Error: "Gagal connect OBS"
- Pastikan OBS Studio sudah running
- Pastikan WebSocket Server sudah enabled
- Pastikan port dan password sesuai dengan konfigurasi OBS

### Error: "Scene not found"
- Pastikan nama scene sudah dibuat di OBS
- Pastikan nama scene **exact match** (case-sensitive)

### Error: "Input not found"
- Pastikan media source sudah dibuat di OBS
- Pastikan nama input **exact match** (case-sensitive)

### Docker: Cannot connect to OBS
- Windows/Mac: Pastikan menggunakan `host.docker.internal:4455`
- Linux: Gunakan IP host machine atau `--network host`

### Error: "password authentication failed for user \"admin\""
PostgreSQL hanya meng-set user/password saat volume **pertama kali** dibuat. Jika volume sudah ada dari konfigurasi lama, kredensial di `docker-compose` tidak dipakai.

**Perbaikan:** Hapus volume Postgres lalu jalankan ulang (data di DB akan hilang):

```bash
cd be
docker compose down -v
docker compose up --build -d
```

Setelah itu koneksi `admin`/`admin` ke `donation_db` akan berfungsi.

## 📝 Notes

- API menggunakan **Gin framework** untuk HTTP server
- Connection ke OBS menggunakan **singleton pattern** (satu connection persistent)
- Semua response menggunakan JSON format
- Error akan return HTTP 500 dengan error message

## 🎯 Next Steps

- [ ] Tambah authentication/API key
- [ ] Tambah webhook support (Saweria, dll)
- [ ] Tambah queue system (Redis)
- [ ] Tambah logging yang lebih detail
- [ ] Tambah rate limiting

## 📄 License

MIT

