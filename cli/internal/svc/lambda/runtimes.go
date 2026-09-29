package lambda

import "strings"

// Runtime is a managed language runtime. Functions run in AWS's official Lambda
// base image for it (public.ecr.aws/lambda/...), which contains the runtime and
// the Runtime Interface Emulator that HomeCloud drives over HTTP.
type Runtime struct {
	Name           string `json:"name"`
	Label          string `json:"label"`
	Image          string `json:"image"`
	DefaultHandler string `json:"default_handler"`
	DefaultFile    string `json:"default_file"`
	// Template is starter code for DefaultFile; empty for compiled runtimes,
	// which need a code package.
	Template   string `json:"template"`
	Deprecated bool   `json:"deprecated,omitempty"`
	// dotted handlers look like file.function (Python, Node.js, Ruby).
	dotted bool
}

const baseImages = "public.ecr.aws/lambda/"

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

const rubyTemplate = `require 'json'

def lambda_handler(event:, context:)
  puts "event: #{event.to_json}"
  name = (event['queryStringParameters'] || {})['name'] || event['name'] || 'world'
  {
    statusCode: 200,
    headers: { 'Content-Type' => 'application/json' },
    body: { message: "Hello, #{name}!", request_id: context.aws_request_id }.to_json
  }
end
`

func py(version string) Runtime {
	return Runtime{Name: "python" + version, Label: "Python " + version, Image: baseImages + "python:" + version,
		DefaultHandler: "lambda_function.lambda_handler", DefaultFile: "lambda_function.py", Template: pythonTemplate, dotted: true}
}

func node(version string) Runtime {
	return Runtime{Name: "nodejs" + version + ".x", Label: "Node.js " + version + ".x", Image: baseImages + "nodejs:" + version,
		DefaultHandler: "index.handler", DefaultFile: "index.mjs", Template: nodeTemplate, dotted: true}
}

func java(version string) Runtime {
	return Runtime{Name: "java" + version, Label: "Java " + version, Image: baseImages + "java:" + version,
		DefaultHandler: "example.Handler::handleRequest"}
}

func provided(tag, label string) Runtime {
	return Runtime{Name: "provided." + tag, Label: label, Image: baseImages + "provided:" + tag, DefaultHandler: "bootstrap"}
}

var runtimes = []Runtime{
	py("3.13"), py("3.12"), py("3.11"), py("3.10"), py("3.9"),
	node("22"), node("20"), node("18"),
	java("21"), java("17"),
	{Name: "ruby3.3", Label: "Ruby 3.3", Image: baseImages + "ruby:3.3", DefaultHandler: "lambda_function.lambda_handler",
		DefaultFile: "lambda_function.rb", Template: rubyTemplate, dotted: true},
	{Name: "dotnet8", Label: ".NET 8", Image: baseImages + "dotnet:8", DefaultHandler: "Function::Function.Handler::FunctionHandler"},
	provided("al2023", "OS-only runtime (Amazon Linux 2023)"),
	provided("al2", "OS-only runtime (Amazon Linux 2)"),
}

func findRuntime(name string) (Runtime, bool) {
	for _, r := range runtimes {
		if r.Name == name {
			return r, true
		}
	}
	return Runtime{}, false
}

// validHandler checks a handler string's shape for the runtime.
func (r Runtime) validHandler(h string) bool {
	if h == "" || len(h) > 128 {
		return false
	}
	if r.dotted {
		i := strings.LastIndexByte(h, '.')
		return i > 0 && i < len(h)-1
	}
	return true
}
