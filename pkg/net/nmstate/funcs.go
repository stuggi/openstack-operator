package nmstate

import (
	nmstateshared "github.com/nmstate/kubernetes-nmstate/api/shared"

	helper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"

	"github.com/tidwall/gjson"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

// GetCurrentCondition - Get current condition with status == corev1.ConditionTrue
func GetCurrentCondition(conditions nmstateshared.ConditionList) *nmstateshared.Condition {
	for i, cond := range conditions {
		if cond.Status == corev1.ConditionTrue {
			return &conditions[i]
		}
	}

	return nil
}

// GetDesiredStateInterfaceStates - Get the states of all interfaces associated with the desiredState
func GetDesiredStateInterfaceStates(h *helper.Helper, desiredStateBytes []byte) (map[string]string, error) {
	interfaceStates := map[string]string{}

	jsonStr, err := GetDesiredStateAsString(desiredStateBytes)
	if err != nil {
		return interfaceStates, err
	}

	if gjson.Get(jsonStr, "interfaces").Exists() {
		interfaces := gjson.Get(jsonStr, "interfaces")

		interfaces.ForEach(func(key, ifc gjson.Result) bool {
			results := gjson.GetMany(ifc.String(), "name", "state")
			interfaceStates[results[0].String()] = results[1].String()

			return true // keep iterating
		})
	}

	return interfaceStates, nil
}

// GetDesiredStateAsString - Get the state as string
func GetDesiredStateAsString(desiredStateBytes []byte) (string, error) {
	jsonStr := ""

	if len(desiredStateBytes) > 0 {
		jsonBytes, err := yaml.YAMLToJSON(desiredStateBytes)

		if err != nil {
			return "", err
		}

		jsonStr = string(jsonBytes)
	}

	return jsonStr, nil
}

/*
// GetDesiredStateBridgeName - Get the name associated with the desiredState bridge interface
func GetDesiredStateBridgeName(desiredStateBytes []byte) (string, error) {
	bridge := ""

	jsonStr, err := GetDesiredStateAsString(desiredStateBytes)
	if err != nil {
		return "", err
	}

	if gjson.Get(jsonStr, "interfaces.#.bridge").Exists() &&
		gjson.Get(jsonStr, "interfaces.#.name").Exists() &&
		len(gjson.Get(jsonStr, `interfaces.#(bridge).name`).Array()) > 0 {

		bridge = gjson.Get(jsonStr, `interfaces.#(bridge).name`).Array()[0].String()
	}

	return bridge, nil
}

// GetDesiredStateBridgeInterfaceState - Get the state associated with the desiredState bridge interface
func GetDesiredStateBridgeInterfaceState(desiredStateBytes []byte) (string, error) {
	state := ""

	jsonStr, err := GetDesiredStateAsString(desiredStateBytes)
	if err != nil {
		return "", err
	}

	if gjson.Get(jsonStr, "interfaces.#.bridge").Exists() &&
		gjson.Get(jsonStr, "interfaces.#.state").Exists() &&
		len(gjson.Get(jsonStr, `interfaces.#(bridge).state`).Array()) > 0 {

		state = gjson.Get(jsonStr, `interfaces.#(bridge).state`).Array()[0].String()
	}

	return state, nil
}
*/
