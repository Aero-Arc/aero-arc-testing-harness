# Chaos Mesh tier

Chaos Mesh is the Kubernetes implementation of faults that require pod,
network, DNS, clock, I/O, or resource control. It is not the scenario API.

The committed workflow is a template. Replace `TEST_RUN_ID` with the unique run
label applied to every test deployment, then apply it only to the dedicated
namespace:

```bash
test_run_id=e2e-$(date -u +%s)
sed "s/TEST_RUN_ID/${test_run_id}/g" deploy/chaos-mesh/workflow.yaml |
  kubectl apply -n aero-arc-e2e -f -
```

Safety requirements:

- use a disposable cluster or dedicated `aero-arc-e2e` namespace;
- every selector includes `aero-arc.io/test-run`;
- every fault has a deadline;
- run the invariant watcher outside the faulted namespace when possible;
- collect the Chaos Mesh workflow status and pod events into the same result
  bundle as the scenario timeline;
- delete the workflow in unconditional cleanup.

The first workflow sequences DSS latency, an API pod kill, and API CPU pressure.
Application-semantic faults such as response loss after upstream commit remain
implemented by the deterministic HTTP fixture/proxy.
