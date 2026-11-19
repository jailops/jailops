package api

// RepairPackagesRequest is the body of POST /api/v1/jails/{name}/repair-packages (extension jail_repair_packages). It
// has no fields: the image generation of the jail's root says what the base package records should be.
type RepairPackagesRequest struct{}

// RepairPackagesResult is the result of the repair-packages operation: the base package records of a thin jail
// brought in step with its image generation (the same pass a start makes).
type RepairPackagesResult struct {
	Jail string `json:"jail"`
	// Base is the number of packages of the base set of the generation; Replaced is how many records had another
	// version and Added how many were missing. All zero in Replaced and Added means the records were in step.
	Base     int `json:"base"`
	Replaced int `json:"replaced"`
	Added    int `json:"added"`
}
