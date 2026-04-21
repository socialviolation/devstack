import fs from "node:fs";
import path from "node:path";
import { execFileSync } from "node:child_process";

export interface RuntimeState {
  service: string;
  state: string;
  pid?: number;
  timestamp?: number;
  mode?: string;
  exit_code?: number;
  port?: number;
}

export interface TelemetrySummary {
  service: string;
  expectedTraces: boolean;
  expectedLogs: boolean;
  mode: string;
  collectorReachable: boolean;
  traceCount: number;
  logEvidence: boolean;
  confidence: "high" | "partial" | "low" | "inconclusive";
  interpretation: string;
}

export function workspaceRoot(cwd: string = process.cwd()): string {
  return cwd;
}

export function readJson<T>(file: string): T | null {
  if (!fs.existsSync(file)) return null;
  return JSON.parse(fs.readFileSync(file, "utf8")) as T;
}

export function workspaceManifest(root: string = workspaceRoot()) {
  return readJsonViaYaml(path.join(root, "devstack.workspace.yaml"));
}

export function serviceManifest(root: string, service: string) {
  return readJsonViaYaml(path.join(root, "services", service, "devstack.service.yaml"));
}

function readJsonViaYaml(file: string): any {
  const content = fs.readFileSync(file, "utf8");
  const temp = path.join(process.cwd(), ".pi", ".tmp-devstack-json");
  fs.mkdirSync(path.dirname(temp), { recursive: true });
  fs.writeFileSync(temp + ".yaml", content, "utf8");
  try {
    const out = execFileSync("python3", [
      "-c",
      "import json, sys, yaml; print(json.dumps(yaml.safe_load(open(sys.argv[1]).read())))",
      temp + ".yaml",
    ], { encoding: "utf8" });
    return JSON.parse(out);
  } finally {
    fs.rmSync(temp + ".yaml", { force: true });
  }
}

export function services(root: string = workspaceRoot()): string[] {
  const manifest = workspaceManifest(root);
  const repos = manifest?.workspace?.repoDiscovery?.repos ?? [];
  return repos.map((repo: string) => path.basename(repo));
}

export function runtimeStates(root: string = workspaceRoot()): Record<string, RuntimeState> {
  const dir = path.join(root, "state", "runtime");
  const result: Record<string, RuntimeState> = {};
  if (!fs.existsSync(dir)) return result;
  for (const entry of fs.readdirSync(dir)) {
    if (!entry.endsWith(".json")) continue;
    const state = readJson<RuntimeState>(path.join(dir, entry));
    if (state?.service) result[state.service] = state;
  }
  return result;
}

export function topology(root: string = workspaceRoot()) {
  const manifest = workspaceManifest(root);
  const runtime = runtimeStates(root);
  const svcNames = services(root);
  const dependencies = manifest?.dependencies ?? {};
  const groups = manifest?.groups ?? {};
  const dependents: Record<string, string[]> = {};
  for (const name of svcNames) dependents[name] = [];
  for (const [service, deps] of Object.entries<Record<string, string[]>>(dependencies)) {
    for (const dep of deps) {
      dependents[dep] ??= [];
      dependents[dep].push(service);
    }
  }
  const serviceInfo = svcNames.map((name) => ({
    service: name,
    path: path.join(root, "services", name),
    groups: Object.entries<Record<string, string[]>>(groups)
      .filter(([, members]) => members.includes(name))
      .map(([group]) => group),
    dependencies: dependencies[name] ?? [],
    dependents: dependents[name] ?? [],
    runtime: runtime[name] ?? null,
  }));
  return {
    workspace: manifest?.workspace?.name ?? path.basename(root),
    root,
    groups,
    services: serviceInfo,
  };
}

export function collectorEntries(root: string = workspaceRoot()): any[] {
  const file = path.join(root, "logs", "collector.jsonl");
  if (!fs.existsSync(file)) return [];
  return fs
    .readFileSync(file, "utf8")
    .split("\n")
    .filter(Boolean)
    .map((line) => JSON.parse(line));
}

export function serviceLogs(root: string, service: string): string {
  const file = path.join(root, "logs", `${service}.log`);
  if (!fs.existsSync(file)) return "";
  return fs.readFileSync(file, "utf8");
}

export function telemetry(root: string = workspaceRoot()): TelemetrySummary[] {
  const entries = collectorEntries(root);
  const states = runtimeStates(root);
  return services(root)
    .filter((service) => service.startsWith("telemetry") || service === "api" || service === "worker" || service === "frontend")
    .map((service) => {
      const manifest = serviceManifest(root, service);
      const expectedTraces = Boolean(manifest?.telemetry?.traces?.expected);
      const expectedLogs = Boolean(manifest?.telemetry?.logs?.expected);
      const modeFile = path.join(root, "state", `${service}.mode`);
      const mode = fs.existsSync(modeFile) ? fs.readFileSync(modeFile, "utf8").trim() || "healthy" : "healthy";
      const traceCount = entries.filter((entry) => entry.body?.service === service || entry.body?.service === `${service}-mismatch`).length;
      const logs = serviceLogs(root, service);
      const logEvidence = logs.length > 0;
      const collectorReachable = !logs.includes("trace export failed") || traceCount > 0;
      let confidence: TelemetrySummary["confidence"] = "inconclusive";
      let interpretation = "No telemetry expectations configured.";
      if (expectedTraces || expectedLogs) {
        if (traceCount > 0 && logEvidence) {
          confidence = "high";
          interpretation = "Observed traces and service logs for the current scenario.";
        } else if ((traceCount > 0 || logEvidence) && collectorReachable) {
          confidence = "partial";
          interpretation = "Observed some evidence, but coverage is incomplete.";
        } else if (!collectorReachable) {
          confidence = "inconclusive";
          interpretation = "Telemetry is inconclusive because collector export failed.";
        } else {
          confidence = "low";
          interpretation = "Expected telemetry was not observed in current artifacts.";
        }
      }
      if (mode === "no-traces" || mode === "logs-only") {
        confidence = logEvidence ? "partial" : "low";
        interpretation = `Scenario mode ${mode} intentionally suppresses traces.`;
      }
      if (mode === "collector-down") {
        confidence = "inconclusive";
        interpretation = "Scenario mode collector-down intentionally makes export unreliable.";
      }
      return {
        service,
        expectedTraces,
        expectedLogs,
        mode,
        collectorReachable,
        traceCount,
        logEvidence,
        confidence,
        interpretation,
      };
    });
}

export function doctor(root: string = workspaceRoot()) {
  const topo = topology(root);
  const issues: string[] = [];
  for (const service of topo.services) {
    for (const dep of service.dependencies) {
      if (!topo.services.find((candidate) => candidate.service === dep)) {
        issues.push(`service ${service.service} depends on missing service ${dep}`);
      }
    }
  }
  return {
    workspace: topo.workspace,
    ok: issues.length === 0,
    issues,
    runtimeCount: Object.keys(runtimeStates(root)).length,
  };
}
