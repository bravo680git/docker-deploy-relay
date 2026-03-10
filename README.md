# Docker Deploy Relay (Go)

Lightweight Webhook Server to coordinate automated deployment: Receive signal from GitHub -> Pull new Docker Image -> Deploy using Docker Compose -> Cleanup Docker Hub tag.

## 🚀 Features

- **Lightweight & Efficient**: Written in Go with minimal dependencies.
- **Asynchronous Execution**: Uses Goroutines for non-blocking responses (202 Accepted).
- **Direct CLI Execution**: Uses `os/exec` to call Docker commands directly.
- **Security**: Verifies requests with an API Key.
- **Automatic Cleanup**: Deletes the used image tag from Docker Hub after a successful pull.

## 🛠 Prerequisites

- Docker and Docker Compose installed on the host.
- A Cloudflare Tunnel (or similar) to expose the relay to the internet.

## 🏗 Setup & Installation

### 1. Project Directory Structure

Each project being deployed should have its own directory containing a `docker-compose.yml` file under the `/apps` directory on the host.

```bash
/apps/
├── project-1/
│   └── docker-compose.yml
└── project-2/
    └── docker-compose.yml
```

### 2. Run with Docker Compose

Create a `docker-compose.yml` for the relay itself:

```yaml
services:
  relay:
    build: .
    container_name: docker-deploy-relay
    ports:
      - "8080:8080"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /path/to/your/apps:/apps
    environment:
      - RELAY_WEBHOOK_API_KEY=your-secure-api-key
      - RELAY_DOCKER_HUB_USER=your-docker-hub-username
      - RELAY_DOCKER_HUB_PASS=your-docker-hub-password
      - RELAY_PROJECT_ROOT=/apps
    restart: unless-stopped
```

### 3. Webhook Payload

Configure your CI (e.g., GitHub Actions) to send a POST request with the following JSON structure:

```json
{
  "project": "project-name",
  "image": "namespace/repository",
  "tag": "v1.0.0"
}
```

Include `X-API-KEY: your-secure-api-key` in the header or `?api_key=your-secure-api-key` in the URL.

Note: the query string option is supported for backwards compatibility, but is discouraged because URLs are often logged by proxies.

### 4. Quick Test with cURL

```bash
curl -X POST https://your-relay-host/webhook \
  -H "Content-Type: application/json" \
  -H "X-API-KEY: your-secure-api-key" \
  -d '{"project": "my-app", "image": "myuser/my-app", "tag": "v1.0.0"}'
```

## 🔧 Environment Variables

| Variable                | Description                                      |
| ----------------------- | ------------------------------------------------ |
| `RELAY_WEBHOOK_API_KEY` | Key used to verify the incoming webhook request. |
| `RELAY_DOCKER_HUB_USER` | Your Docker Hub username for authentication.     |
| `RELAY_DOCKER_HUB_PASS` | Your Docker Hub password (or access token).      |
| `RELAY_PROJECT_ROOT`    | Base path for projects (default: `/apps`).       |

### Optional safety limits

| Variable                       | Description                                                               |
| ------------------------------ | ------------------------------------------------------------------------- |
| `RELAY_MAX_CONCURRENT_DEPLOYS` | Max number of deployments running at once (default: `2`).                 |
| `RELAY_DEPLOY_TIMEOUT`         | Max total time for a deployment (e.g. `15m`). Default: `15m`.             |
| `RELAY_DOCKER_PULL_TIMEOUT`    | Timeout for `docker pull` (e.g. `10m`). Default: `10m`.                   |
| `RELAY_DOCKER_COMPOSE_TIMEOUT` | Timeout for `docker compose up -d` (e.g. `10m`). Default: `10m`.          |
| `RELAY_HUB_TIMEOUT`            | Timeout for Docker Hub tag deletion step (e.g. `20s`). Default: `20s`.    |
| `RELAY_HUB_HTTP_TIMEOUT`       | HTTP client timeout for Docker Hub requests (e.g. `10s`). Default: `10s`. |
| `RELAY_RATE_LIMIT_RPS`         | Per-IP request rate (tokens/sec). Default: `1`.                           |
| `RELAY_RATE_LIMIT_BURST`       | Per-IP burst capacity. Default: `5`.                                      |
| `RELAY_PORT`                   | Port the server listens on (default: `8080`).                             |

## API Reference

### POST /webhook

Triggers a new deployment. Returns `202 Accepted` immediately; the actual work runs asynchronously.

**Headers**: `X-API-KEY: <key>` (required), `Content-Type: application/json`

**Request body**:

```json
{
  "project": "my-app",
  "image": "myuser/my-app",
  "tag": "v1.0.0"
}
```

**Response** (`202 Accepted`):

```json
{
  "deploy_id": "a1b2c3d4e5f60718",
  "status": "running"
}
```

Use the `deploy_id` to poll `GET /deploy-status/{id}` for the final result.

**Error responses**:

| Status                  | Meaning                                                |
| ----------------------- | ------------------------------------------------------ |
| `400 Bad Request`       | Missing or invalid JSON payload                        |
| `401 Unauthorized`      | Missing or invalid API key                             |
| `409 Conflict`          | A deployment for this project is already in progress   |
| `429 Too Many Requests` | Rate limit exceeded or concurrent deploy limit reached |

---

### GET /deploy-status/{id}

Returns the current status of a deployment.

**Headers**: `X-API-KEY: <key>` (required)

**Path parameter**: `{id}` — 16-character lowercase hex string returned by `POST /webhook`.

**Response — in progress** (`200 OK`):

```json
{
  "deploy_id": "a1b2c3d4e5f60718",
  "project": "my-app",
  "image": "myuser/my-app",
  "tag": "v1.0.0",
  "status": "running",
  "phase": "compose_up",
  "created_at": "2026-03-10T12:00:00Z"
}
```

**Response — finished** (`200 OK`):

```json
{
  "deploy_id": "a1b2c3d4e5f60718",
  "project": "my-app",
  "image": "myuser/my-app",
  "tag": "v1.0.0",
  "status": "success",
  "created_at": "2026-03-10T12:00:00Z",
  "done_at": "2026-03-10T12:01:30Z"
}
```

| Field        | Type    | Description                                                                            |
| ------------ | ------- | -------------------------------------------------------------------------------------- |
| `deploy_id`  | string  | Unique identifier for this deployment                                                  |
| `project`    | string  | Project name from the webhook payload                                                  |
| `image`      | string  | Docker image from the webhook payload                                                  |
| `tag`        | string  | Image tag from the webhook payload                                                     |
| `status`     | string  | `running` \| `success` \| `failed`                                                     |
| `phase`      | string  | Active step while `status` is `running`: `pulling` \| `compose_up`. Omitted when done. |
| `error`      | string  | Present only when `status` is `failed`                                                 |
| `created_at` | RFC3339 | When the deployment started                                                            |
| `done_at`    | RFC3339 | When the deployment finished (omitted while `running`)                                 |

**Error responses**:

| Status                  | Meaning                                        |
| ----------------------- | ---------------------------------------------- |
| `400 Bad Request`       | Invalid or missing deploy ID format            |
| `401 Unauthorized`      | Missing or invalid API key                     |
| `404 Not Found`         | Deploy ID not found (expired or never existed) |
| `429 Too Many Requests` | Rate limit exceeded                            |

> **Note**: Completed results (`success` / `failed`) are retained in memory for **5 minutes** after `done_at`. Deployments stuck in `running` for more than **30 minutes** are automatically evicted.

**cURL example**:

```bash
curl https://your-relay-host/deploy-status/a1b2c3d4e5f60718 \
  -H "X-API-KEY: your-secure-api-key"
```

---

## 📦 Deployment Flow

1. **Pull**: Pulls the new image from Docker Hub.
2. **Deploy**: Navigates to the project folder and runs `docker compose up -d`.
3. **Cleanup**: Logs into Docker Hub, gets a JWT, and deletes the specified tag.
