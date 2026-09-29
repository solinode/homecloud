package cfn

import "net/url"

// typeSpec maps a template resource type onto the HomeCloud API. Properties
// are sent as the create request body unchanged, so they use the API's own
// field names (see docs/api.md).
type typeSpec struct {
	Create     string // "POST /api/v1/..."
	IDField    string // field of the create response holding the physical ID
	Get        string // path template with {id}; response is merged into attributes
	Delete     string // "DELETE /api/v1/.../{id}"
	WaitField  string // when set, poll Get until this field is in Ready
	Ready      []string
	Failed     []string
	ArrayFirst bool // the create response is an array; use its first element
}

func esc(s string) string { return url.PathEscape(s) }

var types = map[string]typeSpec{
	"HC::S3::Bucket":                  {Create: "POST /api/v1/s3/buckets", IDField: "name", Get: "/api/v1/s3/buckets/{id}", Delete: "DELETE /api/v1/s3/buckets/{id}?force=true"},
	"HC::SQS::Queue":                  {Create: "POST /api/v1/sqs/queues", IDField: "name", Get: "/api/v1/sqs/queues/{id}", Delete: "DELETE /api/v1/sqs/queues/{id}"},
	"HC::SNS::Topic":                  {Create: "POST /api/v1/sns/topics", IDField: "name", Get: "/api/v1/sns/topics/{id}", Delete: "DELETE /api/v1/sns/topics/{id}"},
	"HC::DynamoDB::Table":             {Create: "POST /api/v1/dynamodb/tables", IDField: "name", Get: "/api/v1/dynamodb/tables/{id}", Delete: "DELETE /api/v1/dynamodb/tables/{id}"},
	"HC::Lambda::Function":            {Create: "POST /api/v1/lambda/functions", IDField: "name", Get: "/api/v1/lambda/functions/{id}", Delete: "DELETE /api/v1/lambda/functions/{id}"},
	"HC::Lambda::EventSourceMapping":  {Create: "POST /api/v1/lambda/event-source-mappings", IDField: "id", Delete: "DELETE /api/v1/lambda/event-source-mappings/{id}"},
	"HC::ApiGateway::Api":             {Create: "POST /api/v1/apigateway/apis", IDField: "id", Get: "/api/v1/apigateway/apis/{id}", Delete: "DELETE /api/v1/apigateway/apis/{id}"},
	"HC::SecretsManager::Secret":      {Create: "POST /api/v1/secrets", IDField: "name", Get: "/api/v1/secrets/{id}", Delete: "DELETE /api/v1/secrets/{id}?force=true"},
	"HC::SSM::Parameter":              {Create: "PUT /api/v1/ssm/parameter", IDField: "@name", Get: "/api/v1/ssm/parameter?name={id}", Delete: "DELETE /api/v1/ssm/parameter?name={id}"},
	"HC::KMS::Key":                    {Create: "POST /api/v1/kms/keys", IDField: "id", Get: "/api/v1/kms/keys/{id}", Delete: "POST /api/v1/kms/keys/{id}/schedule-deletion"},
	"HC::Events::Rule":                {Create: "PUT /api/v1/events/rules/{name}", IDField: "name", Get: "/api/v1/events/rules/{id}", Delete: "DELETE /api/v1/events/rules/{id}"},
	"HC::StepFunctions::StateMachine": {Create: "POST /api/v1/sfn/state-machines", IDField: "name", Get: "/api/v1/sfn/state-machines/{id}", Delete: "DELETE /api/v1/sfn/state-machines/{id}"},
	"HC::EC2::SecurityGroup":          {Create: "POST /api/v1/vpc/security-groups", IDField: "id", Get: "/api/v1/vpc/security-groups/{id}", Delete: "DELETE /api/v1/vpc/security-groups/{id}"},
	"HC::EC2::VPC":                    {Create: "POST /api/v1/vpc/vpcs", IDField: "id", Get: "/api/v1/vpc/vpcs/{id}", Delete: "DELETE /api/v1/vpc/vpcs/{id}"},
	"HC::EC2::Subnet":                 {Create: "POST /api/v1/vpc/subnets", IDField: "id", Delete: "DELETE /api/v1/vpc/subnets/{id}"},
	"HC::EC2::Volume":                 {Create: "POST /api/v1/ec2/volumes", IDField: "id", Get: "/api/v1/ec2/volumes/{id}", Delete: "DELETE /api/v1/ec2/volumes/{id}"},
	"HC::EFS::FileSystem":             {Create: "POST /api/v1/efs/file-systems", IDField: "id", Get: "/api/v1/efs/file-systems/{id}", Delete: "DELETE /api/v1/efs/file-systems/{id}"},
	"HC::EC2::Instance": {Create: "POST /api/v1/ec2/instances", IDField: "id", ArrayFirst: true, Get: "/api/v1/ec2/instances/{id}", Delete: "DELETE /api/v1/ec2/instances/{id}",
		WaitField: "state", Ready: []string{"running"}, Failed: []string{"terminated"}},
	"HC::RDS::DBInstance": {Create: "POST /api/v1/rds/instances", IDField: "id", Get: "/api/v1/rds/instances/{id}", Delete: "DELETE /api/v1/rds/instances/{id}",
		WaitField: "status", Ready: []string{"available"}, Failed: []string{"failed"}},
	"HC::ELB::TargetGroup": {Create: "POST /api/v1/elb/target-groups", IDField: "name", Get: "/api/v1/elb/target-groups/{id}", Delete: "DELETE /api/v1/elb/target-groups/{id}"},
	"HC::ELB::LoadBalancer": {Create: "POST /api/v1/elb/load-balancers", IDField: "name", Get: "/api/v1/elb/load-balancers/{id}", Delete: "DELETE /api/v1/elb/load-balancers/{id}",
		WaitField: "state", Ready: []string{"active"}, Failed: []string{"failed"}},
	"HC::ECR::Repository":     {Create: "POST /api/v1/ecr/repositories", IDField: "name", Get: "/api/v1/ecr/repositories/{id}", Delete: "DELETE /api/v1/ecr/repositories/{id}?force=true"},
	"HC::ECS::TaskDefinition": {Create: "POST /api/v1/ecs/task-definitions", IDField: "@family:revision", Get: "/api/v1/ecs/task-definitions/{id}", Delete: "DELETE /api/v1/ecs/task-definitions/{id}"},
	"HC::ECS::Service":        {Create: "POST /api/v1/ecs/services", IDField: "name", Get: "/api/v1/ecs/services/{id}", Delete: "DELETE /api/v1/ecs/services/{id}"},
	"HC::IAM::Policy":         {Create: "POST /api/v1/iam/policies", IDField: "name", Get: "/api/v1/iam/policies/{id}", Delete: "DELETE /api/v1/iam/policies/{id}"},
	"HC::IAM::Group":          {Create: "POST /api/v1/iam/groups", IDField: "name", Get: "/api/v1/iam/groups/{id}", Delete: "DELETE /api/v1/iam/groups/{id}"},
	"HC::IAM::User":           {Create: "POST /api/v1/iam/users", IDField: "name", Get: "/api/v1/iam/users/{id}", Delete: "DELETE /api/v1/iam/users/{id}"},
	"HC::CloudWatch::Alarm":   {Create: "PUT /api/v1/cloudwatch/alarms/{name}", IDField: "name", Delete: "DELETE /api/v1/cloudwatch/alarms/{id}"},
	"HC::Logs::LogGroup":      {Create: "POST /api/v1/logs/groups", IDField: "name", Delete: "DELETE /api/v1/logs/groups/{id}"},
}
