package lambda

// Runtime describes a language runtime: the image functions run in and the
// bootstrap that loads the handler, reads the event on stdin and writes the
// result as JSON to stdout. Function logs go to stderr.
type Runtime struct {
	Name           string `json:"name"`
	Label          string `json:"label"`
	Image          string `json:"image"`
	DefaultHandler string `json:"default_handler"`
	DefaultFile    string `json:"default_file"`
	Template       string `json:"template"`
	bootstrapFile  string
	bootstrap      string
	invoke         []string
}

const pythonBootstrap = `import sys, os, json, time, importlib, traceback
real_stdout = sys.stdout
sys.stdout = sys.stderr
sys.path.insert(0, "/var/task")
started = time.time()
class Context:
    def __init__(self):
        self.function_name = os.environ.get("HC_FUNCTION_NAME", "")
        self.function_version = "$LATEST"
        self.invoked_function_arn = os.environ.get("HC_FUNCTION_ARN", "")
        self.memory_limit_in_mb = int(os.environ.get("HC_FUNCTION_MEMORY", "128"))
        self.aws_request_id = os.environ.get("HC_REQUEST_ID", "")
        self.log_group_name = os.environ.get("HC_LOG_GROUP", "")
        self.log_stream_name = os.environ.get("HC_LOG_STREAM", "")
        self._deadline = started + float(os.environ.get("HC_FUNCTION_TIMEOUT", "3"))
    def get_remaining_time_in_millis(self):
        return max(0, int((self._deadline - time.time()) * 1000))
def emit(obj):
    real_stdout.write(json.dumps(obj, default=str))
    real_stdout.flush()
try:
    raw = sys.stdin.read()
    event = json.loads(raw) if raw.strip() else {}
    module_name, fn_name = os.environ["HC_HANDLER"].rsplit(".", 1)
    fn = getattr(importlib.import_module(module_name.replace("/", ".")), fn_name)
    emit({"ok": True, "result": fn(event, Context())})
except Exception as e:
    traceback.print_exc()
    emit({"ok": False, "error": {"errorMessage": str(e), "errorType": type(e).__name__,
          "stackTrace": traceback.format_exception(type(e), e, e.__traceback__)}})
`

const nodeBootstrap = `const util = require("util");
const path = require("path");
const { pathToFileURL } = require("url");
const fs = require("fs");
const log = (...a) => process.stderr.write(util.format(...a) + "\n");
console.log = console.info = console.debug = log;
console.warn = console.error = log;
const started = Date.now();
const emit = (o) => new Promise((r) => process.stdout.write(JSON.stringify(o === undefined ? null : o), r));
(async () => {
  try {
    const raw = fs.readFileSync(0, "utf8");
    const event = raw.trim() ? JSON.parse(raw) : {};
    const h = process.env.HC_HANDLER;
    const dot = h.lastIndexOf(".");
    const modPath = h.slice(0, dot), fnName = h.slice(dot + 1);
    let file = ["", ".js", ".mjs", ".cjs"].map((e) => path.join("/var/task", modPath + e)).find((f) => fs.existsSync(f) && fs.statSync(f).isFile());
    if (!file) throw new Error("Cannot find module '" + modPath + "' in /var/task");
    const mod = await import(pathToFileURL(file).href);
    const fn = mod[fnName] || (mod.default && mod.default[fnName]);
    if (typeof fn !== "function") throw new Error("Handler '" + fnName + "' is not exported by " + modPath);
    const timeout = parseFloat(process.env.HC_FUNCTION_TIMEOUT || "3") * 1000;
    const context = {
      functionName: process.env.HC_FUNCTION_NAME, functionVersion: "$LATEST", invokedFunctionArn: process.env.HC_FUNCTION_ARN,
      memoryLimitInMB: process.env.HC_FUNCTION_MEMORY, awsRequestId: process.env.HC_REQUEST_ID,
      logGroupName: process.env.HC_LOG_GROUP, logStreamName: process.env.HC_LOG_STREAM,
      getRemainingTimeInMillis: () => Math.max(0, started + timeout - Date.now()),
    };
    const result = await new Promise((resolve, reject) => {
      const cb = (err, res) => (err ? reject(err) : resolve(res));
      const r = fn(event, context, cb);
      if (r && typeof r.then === "function") r.then(resolve, reject);
    });
    await emit({ ok: true, result: result === undefined ? null : result });
  } catch (e) {
    log(e && e.stack ? e.stack : String(e));
    await emit({ ok: false, error: { errorMessage: e && e.message ? e.message : String(e), errorType: (e && e.name) || "Error", stackTrace: e && e.stack ? e.stack.split("\n") : [] } });
  }
})();
`

const pythonTemplate = `import json

def lambda_handler(event, context):
    print("event:", json.dumps(event))
    name = (event.get("queryStringParameters") or {}).get("name") or event.get("name") or "world"
    return {
        "statusCode": 200,
        "headers": {"Content-Type": "application/json"},
        "body": json.dumps({"message": f"Hello, {name}!", "request_id": context.aws_request_id}),
    }
`

const nodeTemplate = `export const handler = async (event, context) => {
  console.log("event:", JSON.stringify(event));
  const name = event.queryStringParameters?.name ?? event.name ?? "world";
  return {
    statusCode: 200,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ message: ` + "`Hello, ${name}!`" + `, requestId: context.awsRequestId }),
  };
};
`

func py(version string) Runtime {
	return Runtime{Name: "python" + version, Label: "Python " + version, Image: "python:" + version + "-slim",
		DefaultHandler: "lambda_function.lambda_handler", DefaultFile: "lambda_function.py", Template: pythonTemplate,
		bootstrapFile: "bootstrap.py", bootstrap: pythonBootstrap, invoke: []string{"python3", "-u", "/opt/homecloud/bootstrap.py"}}
}

func node(version string) Runtime {
	return Runtime{Name: "nodejs" + version + ".x", Label: "Node.js " + version + ".x", Image: "node:" + version + "-slim",
		DefaultHandler: "index.handler", DefaultFile: "index.mjs", Template: nodeTemplate,
		bootstrapFile: "bootstrap.cjs", bootstrap: nodeBootstrap, invoke: []string{"node", "/opt/homecloud/bootstrap.cjs"}}
}

var runtimes = []Runtime{py("3.13"), py("3.12"), py("3.11"), node("22"), node("20")}

func findRuntime(name string) (Runtime, bool) {
	for _, r := range runtimes {
		if r.Name == name {
			return r, true
		}
	}
	return Runtime{}, false
}
