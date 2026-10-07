import json


def handler(event, context):
    path = event.get("rawPath") or event.get("path")
    return {
        "statusCode": 200,
        "headers": {"content-type": "application/json"},
        "body": json.dumps({"service": "compat-api", "path": path}),
    }
