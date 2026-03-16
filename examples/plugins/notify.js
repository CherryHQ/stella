// notify.js — Plugin that logs a terminal bell on session end and after tool errors.
// The bell character (\x07) triggers an audible/visual notification in most terminals.
//
// Usage: anna plugin add examples/plugins/notify.js

anna.on("session_end", function(event) {
    anna.log("info", "\x07[notify] session ended — " + event.sessionId);
});

anna.on("after_tool_call", function(event) {
    if (event.isError) {
        anna.log("warn", "\x07[notify] tool error — " + event.toolName);
    }
});

anna.registerTool({
    name: "notify",
    description: "Sends a terminal bell notification with a custom message.",
    parameters: {
        type: "object",
        properties: {
            message: { type: "string", description: "Notification message" }
        }
    },
    execute: function(args) {
        var msg = args.message || "notification";
        anna.log("info", "\x07[notify] " + msg);
        return "notified: " + msg;
    }
});
