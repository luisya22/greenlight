package data

import (
	"fmt"
	"strconv"
)

// Runtime Declare a custom Runtime type, which has the underlying type int32 (the sames as our Movie struct field).
type Runtime int32

// MarshalJSON Implement a MarshalJSON() method on the Runtime type so that is satisfies the json.Marshaler interface.
// This should return the JSON-encoded value for the movie runtime (in our case, it will return a string in the
// format "<runtime> mins").
func (r Runtime) MarshalJSON() ([]byte, error) {
	// Generate a string containing the movie runtime in the required format.
	jsonValue := fmt.Sprintf("%d mins", r)

	// Use the strconv.Quote() function on the string to wrap it in double quotes. It needs to be surrounded
	// by double quotes in order to be valid *JSON string*
	quotedJSONValue := strconv.Quote(jsonValue)

	// Convert the quotes string value to a byte slice and return it
	return []byte(quotedJSONValue), nil
}
