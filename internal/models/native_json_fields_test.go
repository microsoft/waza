package models_test

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

type promotedJSONIDs struct {
	ClusterID string `json:"cluster_id"`
}

type PromotedJSONIDs struct {
	ClusterID string `json:"cluster_id"`
}

type promotedJSONValue struct {
	Known string `json:"known"`
}

type PromotedJSONTagged struct {
	Value promotedJSONValue `json:"Value"`
}

type promotedJSONUntagged struct {
	Value string
}

type PromotedJSONSameTag struct {
	Value promotedJSONValue `json:"Value"`
}

type promotedJSONBranchA struct {
	promotedJSONIDs
}

type promotedJSONBranchB struct {
	promotedJSONIDs
}

type recursiveJSONFields struct {
	*recursiveJSONFields
	Value string `json:"value"`
}

type promotedJSONIdentifier string
type PromotedJSONIdentifier string

func TestNativeJSONPromotedFieldsMatchDecoderSelection(t *testing.T) {
	taggedValue := func(name string) any {
		kind := reflect.StructOf([]reflect.StructField{{
			Name: "Value", Type: reflect.TypeFor[string](), Tag: reflect.StructTag("json:" + strconv.Quote(name)),
		}})
		return reflect.Zero(kind).Interface()
	}
	ambiguous := reflect.StructOf([]reflect.StructField{
		{Name: "PromotedJSONTagged", Type: reflect.TypeFor[PromotedJSONTagged](), Anonymous: true},
		{Name: "PromotedJSONSameTag", Type: reflect.TypeFor[PromotedJSONSameTag](), Anonymous: true},
	})
	for _, test := range []struct {
		name   string
		target any
		good   string
		bad    string
	}{
		{"unexported embedded struct", struct{ promotedJSONIDs }{}, `{"cluster_id":"c"}`, `{"CLUSTER_ID":"c"}`},
		{"embedded pointer", struct{ *PromotedJSONIDs }{}, `{"cluster_id":"c"}`, `{"cluster_id":"c","Cluster_ID":"other"}`},
		{"tagged embedded struct", struct {
			promotedJSONIDs `json:"key"`
		}{}, `{"key":{"cluster_id":"c"}}`, `{"key":{"CLUSTER_ID":"c"}}`},
		{"tag wins at equal depth", struct {
			PromotedJSONTagged
			promotedJSONUntagged
		}{}, `{"Value":{"known":"v"}}`, `{"Value":{"Known":"v"}}`},
		{"shallower field wins", struct {
			PromotedJSONTagged
			Value string
		}{}, `{"Value":"v","unknown":{"Known":"opaque"}}`, `{"value":"v"}`},
		{"ambiguous tagged fields ignored", reflect.Zero(ambiguous).Interface(), `{"Value":{"Known":"opaque"},"value":{"Known":"also opaque"}}`, ""},
		{"repeated promoted type ignored", struct {
			promotedJSONBranchA
			promotedJSONBranchB
		}{}, `{"cluster_id":"c","CLUSTER_ID":"ignored"}`, ""},
		{"recursive embedding terminates", recursiveJSONFields{recursiveJSONFields: &recursiveJSONFields{Value: "shadowed"}}, `{"value":"v"}`, `{"VALUE":"v"}`},
		{"embedded scalar is named", struct{ PromotedJSONIdentifier }{}, `{"PromotedJSONIdentifier":"v"}`, `{"PROMOTEDJSONIDENTIFIER":"v"}`},
		{"unexported embedded scalar ignored", struct{ promotedJSONIdentifier }{}, `{"unknown":"v"}`, ""},
		{"invalid tag falls back to Go name", taggedValue(`"invalid`), `{"Value":"v"}`, `{"value":"v"}`},
		{"dash tag option is a name", taggedValue("-,omitempty"), `{"-":"v"}`, ""},
		{"ignored field remains unknown", struct {
			Value string `json:"-"`
		}{}, `{"Value":"v","value":"opaque"}`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.NoError(t, models.ValidateNativeJSONKeys([]byte(test.good), test.target))
			destination := reflect.New(reflect.TypeOf(test.target)).Interface()
			require.NoError(t, json.Unmarshal([]byte(test.good), destination))
			if test.bad != "" {
				require.ErrorContains(t, models.ValidateNativeJSONKeys([]byte(test.bad), test.target), "exact spelling")
			}
		})
	}
}
