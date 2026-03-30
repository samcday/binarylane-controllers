use std::time::Duration;

use anyhow::{Result, bail};
use integration_tests::{TestContext, wait_for};
use k8s_openapi::api::apps::v1::Deployment;
use k8s_openapi::api::core::v1::Node;
use kube::Api;
use kube::api::{ListParams, Patch, PatchParams};

#[tokio::test]
async fn test_autoscaler_scale_up_down() -> Result<()> {
    let ctx = match TestContext::new().await {
        Some(ctx) => ctx,
        None => {
            eprintln!("skipping: BL_API_TOKEN or KUBECONFIG not set");
            return Ok(());
        }
    };

    // Get initial server count from BL API.
    let initial_servers = ctx.bl.list_servers().await?;
    let initial_count = initial_servers.len();
    eprintln!("initial server count: {initial_count}");

    // Count initial worker nodes.
    let nodes: Api<Node> = Api::all(ctx.kube.clone());
    let initial_workers = nodes
        .list(&ListParams::default().labels("autoscale-group=workers"))
        .await?
        .items
        .len();
    eprintln!("initial worker node count: {initial_workers}");

    // Scale the scale-test deployment to 2 replicas.
    let deploys: Api<Deployment> = Api::namespaced(ctx.kube.clone(), "default");
    let patch = serde_json::json!({"spec": {"replicas": 2}});
    deploys
        .patch("scale-test", &PatchParams::default(), &Patch::Merge(&patch))
        .await?;
    eprintln!("scaled scale-test deployment to 2 replicas");

    // Wait for new worker nodes to appear.
    let kube_up = ctx.kube.clone();
    wait_for(Duration::from_secs(300), Duration::from_secs(10), || {
        let kube = kube_up.clone();
        async move {
            let nodes: Api<Node> = Api::all(kube);
            let workers = nodes
                .list(&ListParams::default().labels("autoscale-group=workers"))
                .await?
                .items;
            let count = workers.len();
            if count <= initial_workers {
                bail!(
                    "waiting for new worker nodes: have {count}, need more than {initial_workers}"
                );
            }
            eprintln!("new worker nodes appeared: {count} (was {initial_workers})");
            Ok(())
        }
    })
    .await?;

    // Verify via BL API that new servers exist.
    let bl_up = ctx.bl.clone();
    wait_for(Duration::from_secs(30), Duration::from_secs(5), || {
        let bl = bl_up.clone();
        async move {
            let servers = bl.list_servers().await?;
            if servers.len() <= initial_count {
                bail!(
                    "waiting for new BL servers: have {}, need more than {initial_count}",
                    servers.len()
                );
            }
            Ok(())
        }
    })
    .await?;

    // Check that the new nodes have the expected label.
    let worker_nodes = nodes
        .list(&ListParams::default().labels("autoscale-group=workers"))
        .await?
        .items;
    for node in &worker_nodes {
        let name = node.metadata.name.as_deref().unwrap_or("<unnamed>");
        let labels = node.metadata.labels.as_ref();
        let has_label = labels
            .and_then(|l| l.get("autoscale-group"))
            .is_some_and(|v| v == "workers");
        assert!(has_label, "node {name} missing autoscale-group=workers");
        eprintln!("verified label on worker node: {name}");
    }

    // Scale back to 0 replicas.
    let patch_down = serde_json::json!({"spec": {"replicas": 0}});
    deploys
        .patch(
            "scale-test",
            &PatchParams::default(),
            &Patch::Merge(&patch_down),
        )
        .await?;
    eprintln!("scaled scale-test deployment to 0 replicas");

    // Wait for extra worker nodes to be removed (autoscaler scale-down).
    let kube_down = ctx.kube.clone();
    wait_for(Duration::from_secs(300), Duration::from_secs(10), || {
        let kube = kube_down.clone();
        async move {
            let nodes: Api<Node> = Api::all(kube);
            let workers = nodes
                .list(&ListParams::default().labels("autoscale-group=workers"))
                .await?
                .items;
            let count = workers.len();
            if count > initial_workers {
                bail!(
                    "waiting for worker nodes to scale down: have {count}, want {initial_workers}"
                );
            }
            eprintln!("worker nodes scaled down: {count}");
            Ok(())
        }
    })
    .await?;

    // Verify BL servers cleaned up.
    let bl_down = ctx.bl.clone();
    wait_for(Duration::from_secs(60), Duration::from_secs(10), || {
        let bl = bl_down.clone();
        async move {
            let servers = bl.list_servers().await?;
            if servers.len() > initial_count {
                bail!(
                    "waiting for BL servers to clean up: have {}, want {initial_count}",
                    servers.len()
                );
            }
            Ok(())
        }
    })
    .await?;

    eprintln!("autoscaler scale-up/down test passed");
    Ok(())
}
