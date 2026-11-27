// Package demo has the placeholder data of the demo mode ("nodespot -demo"):
// a node list and a radioid.net user list. Every name, callsign and node
// number in it is made up.
package demo

import "github.com/w1jar/nodespot/internal/nodelist"

// LocalNode is the demo station's AllStar node number.
const LocalNode = "1997"

// NodeList returns the built-in fake node database.
func NodeList() []nodelist.Info {
	return []nodelist.Info{
		{Node: "1997", Callsign: "N0CALL", Description: "Demo node (this node)", Location: "Demoville, ST"},
		{Node: "1998", Callsign: "N1CALL", Description: "Demo hub", Location: "Springfield, ST"},
		{Node: "1999", Callsign: "N2CALL", Description: "Demo test hub", Location: "Metropolis, ST"},
		{Node: "2000", Callsign: "N3CALL", Description: "Demo reflector", Location: "Nowhere, ST"},
		{Node: "2001", Callsign: "N0DEMO", Description: "Demo club node", Location: "Shelbyville, ST"},
		{Node: "2002", Callsign: "N0TEST", Description: "Demo repeater", Location: "Anytown, ST"},
	}
}
