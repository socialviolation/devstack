import type { ExtensionAPI } from "@mariozechner/pi-coding-agent";
import { Type } from "@sinclair/typebox";
import { doctor, telemetry, topology, runtimeStates } from "./lib/workspace";

function print(data: unknown) {
  console.log(JSON.stringify(data, null, 2));
}

export default function devstackPlayground(pi: ExtensionAPI) {
  pi.registerTool({
    name: "workspace_topology",
    label: "Workspace Topology",
    description: "Return playground services, groups, dependencies, dependents, and runtime state.",
    parameters: Type.Object({}),
    async execute() {
      const data = topology();
      return {
        content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
        details: data,
      };
    },
  });

  pi.registerTool({
    name: "workspace_status",
    label: "Workspace Status",
    description: "Return current runtime state for playground services from runtime state files.",
    parameters: Type.Object({}),
    async execute() {
      const data = { runtime: runtimeStates(), topology: topology() };
      return {
        content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
        details: data,
      };
    },
  });

  pi.registerTool({
    name: "telemetry_health",
    label: "Telemetry Health",
    description: "Report evidence-based telemetry confidence for playground services.",
    parameters: Type.Object({}),
    async execute() {
      const data = telemetry();
      return {
        content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
        details: data,
      };
    },
  });

  pi.registerCommand("devstack-topology", {
    description: "Print playground topology as JSON",
    handler: async () => print(topology()),
  });

  pi.registerCommand("devstack-status", {
    description: "Print playground runtime status as JSON",
    handler: async () => print({ runtime: runtimeStates(), topology: topology() }),
  });

  pi.registerCommand("devstack-telemetry", {
    description: "Print evidence-based telemetry health as JSON",
    handler: async () => print(telemetry()),
  });

  pi.registerCommand("devstack-doctor", {
    description: "Print playground doctor report as JSON",
    handler: async () => print(doctor()),
  });

  pi.registerCommand("dashboard", {
    description: "Show a playground dashboard summary",
    handler: async (_args, ctx) => {
      const report = {
        topology: topology(),
        telemetry: telemetry(),
        doctor: doctor(),
      };
      if (!ctx.hasUI) {
        print(report);
        return;
      }
      ctx.ui.notify(`playground: ${report.topology.services.length} services`, "info");
      print(report);
    },
  });

  pi.registerCommand("devstack-commands", {
    description: "Print loaded extension and skill commands",
    handler: async () => {
      const commands = pi.getCommands().map((command) => ({
        name: command.name,
        source: command.source,
        path: command.sourceInfo.path,
      }));
      print(commands);
    },
  });

  pi.registerCommand("devstack-tools", {
    description: "Print loaded tools including playground extension tools",
    handler: async () => {
      const tools = pi.getAllTools().map((tool) => ({
        name: tool.name,
        source: tool.sourceInfo.source,
        path: tool.sourceInfo.path,
      }));
      print(tools);
    },
  });

  pi.on("before_agent_start", async (event) => ({
    systemPrompt:
      event.systemPrompt +
      `\n\nPLAYGROUND TRUTHFULNESS RULES:\n- Missing telemetry is not proof that an action did not happen.\n- Use the workspace_topology tool before making dependency claims.\n- Use workspace_status and telemetry_health before concluding a service is healthy or broken.\n- When traces or logs are missing, say the evidence is inconclusive unless stronger evidence exists.\n- Prefer phrases like \"I found no matching traces in the current artifacts\" over definitive absence claims.`,
  }));
}
