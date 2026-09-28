package terraform

import rego.v1

test_forbidden_greeting_denied if {
	count(deny) == 1 with input as {"resource_changes": [{"address": "a", "type": "terraform_data", "change": {"after": {"input": "bye"}}}]}
		with data.forbidden as ["bye"]
}

test_greeting_allowed if {
	count(deny) == 0 with input as {"resource_changes": [{"address": "a", "type": "terraform_data", "change": {"after": {"input": "hello"}}}]}
		with data.forbidden as ["bye"]
}
