// lifecycle-logger.js — Example plugin that logs every lifecycle event.
// Usage: anna plugin add examples/plugins/lifecycle-logger.js

anna.on("session_start", function(event) {
    anna.log("info", "[lifecycle] session_start — id=" + event.sessionId + " channel=" + event.channel);
});

anna.on("session_end", function(event) {
    anna.log("info", "[lifecycle] session_end — id=" + event.sessionId + " channel=" + event.channel);
});

anna.on("before_tool_call", function(event) {
    anna.log("info", "[lifecycle] before_tool_call — tool=" + event.toolName + " args=" + JSON.stringify(event.arguments));
});

anna.on("after_tool_call", function(event) {
    var status = event.isError ? "ERROR" : "OK";
    anna.log("info", "[lifecycle] after_tool_call — tool=" + event.toolName + " status=" + status);
});

// Register a simple tool so the plugin shows up in the tool list.
anna.registerTool({
    name: "lifecycle_ping",
    description: "Returns pong. Use this to verify the lifecycle-logger plugin is loaded.",
    parameters: { type: "object", properties: {} },
    execute: function(args) {
        anna.log("info", "[lifecycle] lifecycle_ping called");
        return "pong";
    }
});
