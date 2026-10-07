import os

import docker
import pytest

from testcontainers_homecloud import DEFAULT_IMAGE, HomeCloudContainer

# A local build of integrations/testdata/Dockerfile, or the official image.
IMAGE = os.environ.get("HOMECLOUD_IMAGE", DEFAULT_IMAGE)


def leftovers(account_id):
    flt = {"label": f"homecloud.account={account_id}"}
    dc = docker.from_env()
    try:
        return (
            len(dc.containers.list(all=True, filters=flt)),
            len(dc.networks.list(filters=flt)),
            len(dc.volumes.list(filters=flt)),
        )
    finally:
        dc.close()


@pytest.fixture(scope="module")
def homecloud():
    hc = HomeCloudContainer(IMAGE)
    with hc:
        yield hc
    assert hc.account_id
    assert leftovers(hc.account_id) == (0, 0, 0), "stop() left HomeCloud resources behind"


def test_connection_details(homecloud):
    assert homecloud.get_endpoint().startswith("http://")
    creds = homecloud.get_credentials()
    assert creds.access_key_id and creds.secret_access_key and creds.region
    assert homecloud.aws_env()["AWS_ENDPOINT_URL"] == homecloud.get_endpoint()
    ident = homecloud.get_client("sts").get_caller_identity()
    assert ident["Account"] == homecloud.account_id


def test_s3(homecloud):
    s3 = homecloud.get_client("s3")
    s3.create_bucket(Bucket="tc-python")
    s3.put_object(Bucket="tc-python", Key="a.txt", Body=b"hello")
    assert s3.get_object(Bucket="tc-python", Key="a.txt")["Body"].read() == b"hello"
    bucket = homecloud.get_resource("s3").Bucket("tc-python")
    assert [o.key for o in bucket.objects.all()] == ["a.txt"]


def test_sqs(homecloud):
    sqs = homecloud.get_client("sqs")
    url = sqs.create_queue(QueueName="tc-python")["QueueUrl"]
    sqs.send_message(QueueUrl=url, MessageBody="hi")
    msgs = sqs.receive_message(QueueUrl=url, WaitTimeSeconds=1)["Messages"]
    assert [m["Body"] for m in msgs] == ["hi"]


def test_dynamodb(homecloud):
    ddb = homecloud.get_resource("dynamodb")
    table = ddb.create_table(
        TableName="tc-python",
        AttributeDefinitions=[{"AttributeName": "id", "AttributeType": "S"}],
        KeySchema=[{"AttributeName": "id", "KeyType": "HASH"}],
        BillingMode="PAY_PER_REQUEST",
    )
    table.put_item(Item={"id": "1", "n": 2})
    assert table.get_item(Key={"id": "1"})["Item"]["n"] == 2


def test_lambda_list(homecloud):
    assert homecloud.get_client("lambda").list_functions()["Functions"] == []
