package terraform

import rego.v1

deny contains msg if {
	some rc in input.resource_changes
	rc.type == "terraform_data"
	rc.change.after.input in data.forbidden
	msg := sprintf("%s says %q, which the policy forbids", [rc.address, rc.change.after.input])
}
