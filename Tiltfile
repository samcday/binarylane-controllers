load("ext://helm_resource", "helm_resource", "helm_repo")

allow_k8s_contexts("default")

registry_image = os.getenv("BL_DEV_CONTROLLER_IMAGE", "ghcr.io/samcday/binarylane-controller")
registry_pull_secret = os.getenv("BL_DEV_REGISTRY_PULL_SECRET", "dev-registry-cred")
binarylane_api_token = os.getenv("BL_API_TOKEN", "")

if binarylane_api_token == "":
    fail(
        "BL_API_TOKEN is empty. Run: eval \"$(BL_API_TOKEN=$(cat ~/.bltoken) cargo xtask dev-up)\" and then retry tilt up."
    )

if registry_image == "ghcr.io/samcday/binarylane-controller":
    print(
        "warning: BL_DEV_CONTROLLER_IMAGE is not set; using GHCR fallback. Run eval \"$(BL_API_TOKEN=$(cat ~/.bltoken) cargo xtask dev-up)\" for remote dev registry wiring."
    )

# Build the container image
docker_build(
    registry_image,
    ".",
    dockerfile="Dockerfile",
    only=["Cargo.toml", "Cargo.lock", "build.rs", "src/", "proto/", "xtask/"],
    live_update=[
        sync("src/", "/src/src/"),
        run("cd /src && cargo build --release", trigger=["src/"]),
    ],
)

# Deploy via Helm chart
k8s_yaml(helm(
    "chart",
    name="binarylane-controller",
    namespace="binarylane-system",
    set=[
        "apiToken=" + binarylane_api_token,
        "image.repository=" + registry_image,
        "image.tag=latest",
        "image.pullPolicy=Always",
        "imagePullSecrets[0].name=" + registry_pull_secret,
    ],
    values=["tilt-values.yaml"],
))

k8s_resource(
    "binarylane-controller",
    port_forwards="8086:8086",
    labels=["controller"],
)
