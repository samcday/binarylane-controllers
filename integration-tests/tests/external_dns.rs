use std::collections::BTreeMap;
use std::time::Duration;

use anyhow::Result;
use binarylane_client::DomainRecord;
use integration_tests::{TestContext, test_name, wait_for};
use k8s_openapi::api::core::v1::Service;
use kube::Api;
use kube::api::{DeleteParams, ObjectMeta, PostParams};

/// Build the FQDN for a domain record (handles bare "@" and empty names).
fn record_fqdn(record: &DomainRecord, domain: &str) -> String {
    if record.name.is_empty() || record.name == "@" {
        domain.to_string()
    } else {
        format!("{}.{domain}", record.name)
    }
}

#[tokio::test]
async fn test_external_dns_record_lifecycle() -> Result<()> {
    let ctx = match TestContext::new().await {
        Some(ctx) => ctx,
        None => {
            eprintln!("skipping: BL_API_TOKEN or KUBECONFIG not set");
            return Ok(());
        }
    };

    // Check that external-dns is deployed in the cluster.
    let deploys: kube::Api<k8s_openapi::api::apps::v1::Deployment> =
        kube::Api::namespaced(ctx.kube.clone(), "binarylane-system");
    match deploys.get("external-dns").await {
        Ok(_) => {}
        Err(kube::Error::Api(e)) if e.code == 404 => {
            eprintln!(
                "skipping: external-dns deployment not found (deploy full stack via Tilt first)"
            );
            return Ok(());
        }
        Err(e) => return Err(e.into()),
    }

    // List BL domains and skip if none are configured.
    let domains = ctx.bl.list_domains().await?;
    if domains.is_empty() {
        eprintln!("skipping: no BL domains configured");
        return Ok(());
    }
    let domain = &domains[0].name;
    eprintln!("using domain: {domain}");

    // Generate a unique subdomain.
    let subdomain = test_name("dns");
    let hostname = format!("{subdomain}.{domain}");
    eprintln!("test hostname: {hostname}");

    // Create a k8s Service of type LoadBalancer with the external-dns annotation.
    let svc_name = subdomain.clone();
    let services: Api<Service> = Api::namespaced(ctx.kube.clone(), "default");

    let svc = Service {
        metadata: ObjectMeta {
            name: Some(svc_name.clone()),
            annotations: Some(BTreeMap::from([(
                "external-dns.alpha.kubernetes.io/hostname".to_string(),
                hostname.clone(),
            )])),
            ..Default::default()
        },
        spec: Some(k8s_openapi::api::core::v1::ServiceSpec {
            type_: Some("LoadBalancer".to_string()),
            ports: Some(vec![k8s_openapi::api::core::v1::ServicePort {
                port: 80,
                target_port: Some(
                    k8s_openapi::apimachinery::pkg::util::intstr::IntOrString::Int(80),
                ),
                protocol: Some("TCP".to_string()),
                ..Default::default()
            }]),
            selector: Some(BTreeMap::from([(
                "app".to_string(),
                "blctest-external-dns-dummy".to_string(),
            )])),
            ..Default::default()
        }),
        ..Default::default()
    };

    services.create(&PostParams::default(), &svc).await?;
    eprintln!("created test service: {svc_name}");

    // Cleanup helper: delete service and leftover DNS records on exit.
    let cleanup = |ctx: &TestContext, svc_name: &str, domain: &str, hostname: &str| {
        let bl = ctx.bl.clone();
        let kube = ctx.kube.clone();
        let svc_name = svc_name.to_string();
        let domain = domain.to_string();
        let hostname = hostname.to_string();
        async move {
            // Delete the service (ignore errors).
            let services: Api<Service> = Api::namespaced(kube, "default");
            let _ = services.delete(&svc_name, &DeleteParams::default()).await;

            // Clean up any leftover DNS records.
            if let Ok(records) = bl.list_domain_records(&domain).await {
                for record in records {
                    let fqdn = record_fqdn(&record, &domain);
                    let is_ours = fqdn == hostname
                        && (record.record_type == "A"
                            || (record.record_type == "TXT"
                                && record.data.contains("heritage=external-dns")));

                    if is_ours {
                        eprintln!(
                            "cleanup: deleting record {} ({} {})",
                            record.id, record.record_type, record.name
                        );
                        let _ = bl.delete_domain_record(&domain, record.id).await;
                    }
                }
            }
        }
    };

    // Wait for the A record to appear.
    let bl_wait_a = ctx.bl.clone();
    let domain_a = domain.clone();
    let hostname_a = hostname.clone();
    let wait_result = wait_for(Duration::from_secs(120), Duration::from_secs(5), || {
        let bl = bl_wait_a.clone();
        let domain = domain_a.clone();
        let hostname = hostname_a.clone();
        async move {
            let records = bl.list_domain_records(&domain).await?;
            let has_a = records
                .iter()
                .any(|r| r.record_type == "A" && record_fqdn(r, &domain) == hostname);
            Ok(has_a)
        }
    })
    .await;

    if let Err(e) = &wait_result {
        eprintln!("A record wait failed: {e}");
        cleanup(&ctx, &svc_name, domain, &hostname).await;
        return Err(wait_result.unwrap_err());
    }
    eprintln!("A record appeared for {hostname}");

    // Check for TXT ownership record (created by external-dns alongside the A record).
    let records = ctx.bl.list_domain_records(domain).await?;
    let has_txt = records.iter().any(|r| {
        r.record_type == "TXT"
            && record_fqdn(r, domain) == hostname
            && r.data.contains("heritage=external-dns")
    });
    if !has_txt {
        eprintln!(
            "warning: no TXT ownership record found for {hostname} (some external-dns configs may not create these)"
        );
    } else {
        eprintln!("TXT ownership record found for {hostname}");
    }

    // Delete the k8s service.
    services.delete(&svc_name, &DeleteParams::default()).await?;
    eprintln!("deleted test service: {svc_name}");

    // Wait for DNS records to be cleaned up.
    let bl_wait_del = ctx.bl.clone();
    let domain_del = domain.clone();
    let hostname_del = hostname.clone();
    let del_result = wait_for(Duration::from_secs(120), Duration::from_secs(5), || {
        let bl = bl_wait_del.clone();
        let domain = domain_del.clone();
        let hostname = hostname_del.clone();
        async move {
            let records = bl.list_domain_records(&domain).await?;
            let still_exists = records.iter().any(|r| {
                record_fqdn(r, &domain) == hostname
                    && (r.record_type == "A" || r.record_type == "TXT")
            });
            Ok(!still_exists)
        }
    })
    .await;

    if let Err(e) = &del_result {
        eprintln!("DNS cleanup wait failed: {e}");
        cleanup(&ctx, &svc_name, domain, &hostname).await;
        return Err(del_result.unwrap_err());
    }

    eprintln!("DNS records cleaned up for {hostname}");
    eprintln!("external-dns record lifecycle test passed");
    Ok(())
}
