import json
import os


def handler(event, context):
    records = event.get("Records", [])
    for r in records:
        print("job:", r.get("body"))
    return {"greeting": os.environ.get("GREETING"), "records": len(records), "echo": event.get("echo")}
