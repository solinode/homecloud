package ec2

import (
	"strings"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
)

func TestLaunchTemplateDataXML(t *testing.T) {
	var b strings.Builder
	awsapi.WriteXML(&b, "d", dataXML(map[string]string{
		"ImageId": "ami-nginx", "SecurityGroupId.1": "sg-1", "SecurityGroupId.2": "sg-2",
		"TagSpecification.1.ResourceType": "instance", "TagSpecification.1.Tag.1.Key": "Name", "TagSpecification.1.Tag.1.Value": "n",
		"Monitoring.Enabled": "true",
	}))
	got := b.String()
	for _, want := range []string{"<imageId>ami-nginx</imageId>", "<securityGroupIdSet><item>sg-1</item><item>sg-2</item></securityGroupIdSet>",
		"<tagSpecificationSet><item><resourceType>instance</resourceType><tagSet><item><key>Name</key><value>n</value></item></tagSet></item></tagSpecificationSet>",
		"<monitoring><enabled>true</enabled></monitoring>"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
}
