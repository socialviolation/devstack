# Navexa Dev Stack — Tiltfile
#
# Usage:
#   tilt up                        # all services, Development env (manual trigger — nothing starts automatically)
#   tilt up -- --env=Staging       # target a different ASP.NET environment
#   tilt up navexa-api nxTradeImporter  # only bring up named services
#
# AI/agent control:
#   tilt get uiresource -o json              # all service statuses as JSON
#   tilt get uiresource navexa-api -o json   # specific service status
#   tilt trigger navexa-api                  # restart a service
#   tilt logs navexa-api                     # stream logs
#   curl localhost:10350/api/view            # full state via HTTP API

DOTNET = "/home/nick/.local/share/mise/installs/dotnet/8.0.418/dotnet"

# ---------------------------------------------------------------------------
# Environment targeting
# Switch with: tilt up -- --env=Staging
# ---------------------------------------------------------------------------
config.define_string('env', args=False, usage='ASP.NET Core environment: Development, Staging, Production')
cfg = config.parse()
ASPNET_ENV = cfg.get('env', os.getenv('ASPNETCORE_ENVIRONMENT', 'Development'))

print("Target environment: " + ASPNET_ENV)

# ---------------------------------------------------------------------------
# Helper: free a TCP port before launching a service
# Runs as the cmd= phase before serve_cmd= starts
# ---------------------------------------------------------------------------
def free_port(port):
    return 'fuser -k {p}/tcp 2>/dev/null; fuser -k -9 {p}/tcp 2>/dev/null; true'.format(p=port)

# ---------------------------------------------------------------------------
# Services
# All are TRIGGER_MODE_MANUAL + auto_init=False — nothing runs until you
# click "trigger" in the UI or run: tilt trigger <name>
# ---------------------------------------------------------------------------

local_resource(
    'nxTradeImporter',
    cmd=free_port(5178),
    serve_cmd=DOTNET + ' run',
    serve_dir='/home/nick/dev/navexa/nxTradeImporter/nxTradeImporter',
    serve_env={'ASPNETCORE_ENVIRONMENT': ASPNET_ENV},
    readiness_probe=probe(
        http_get=http_get_action(port=5178),
        period_secs=5,
        failure_threshold=12,
    ),
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    labels=['dotnet'],
    links=[link('http://localhost:5178', 'nxTradeImporter')],
)

local_resource(
    'nxFileImporter',
    cmd=free_port(5001),
    serve_cmd=DOTNET + ' run',
    serve_dir='/home/nick/dev/navexa/nxFileImporter/nxFileImporter',
    serve_env={'ASPNETCORE_ENVIRONMENT': ASPNET_ENV},
    readiness_probe=probe(
        http_get=http_get_action(port=5001),
        period_secs=5,
        failure_threshold=12,
    ),
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    labels=['dotnet'],
    links=[link('http://localhost:5001', 'nxFileImporter')],
)

local_resource(
    'ai-file-importer',
    serve_cmd='bash -c "source .envrc && python main.py"',
    serve_dir='/home/nick/dev/navexa/ai-file-importer',
    serve_env={'APP_ENV': ASPNET_ENV},
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    labels=['python'],
)

local_resource(
    'navexa-api',
    cmd=free_port(63290),
    serve_cmd=DOTNET + ' run',
    serve_dir='/home/nick/dev/navexa/Navexa/src/Navexa.API',
    serve_env={'ASPNETCORE_ENVIRONMENT': ASPNET_ENV},
    readiness_probe=probe(
        http_get=http_get_action(port=63290),
        period_secs=5,
        failure_threshold=12,
    ),
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    labels=['dotnet'],
    links=[link('http://localhost:63290', 'Navexa API')],
)

local_resource(
    'navexa-frontend',
    cmd=free_port(4200),
    serve_cmd='npm run start-dock',
    serve_dir='/home/nick/dev/navexa/NavexaFrontEnd',
    readiness_probe=probe(
        http_get=http_get_action(port=4200),
        period_secs=5,
        failure_threshold=24,  # npm start is slow
    ),
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    labels=['frontend'],
    links=[link('http://localhost:4200', 'Frontend')],
)

# ---------------------------------------------------------------------------
# SSH Tunnel (equivalent to Shift+F in dev.py)
# Forwards: :4200  :63290  :5178  via nick@100.78.180.51
# ---------------------------------------------------------------------------
local_resource(
    'ssh-tunnel',
    serve_cmd='ssh -N -o ServerAliveInterval=30 -L 4200:localhost:4200 -L 63290:localhost:63290 -L 5178:localhost:5178 nick@100.78.180.51',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    labels=['infra'],
)
