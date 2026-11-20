package routes

import (
	fiber "github.com/gofiber/fiber/v3"

	models "vocabtrainer/server/models"
)

// teamMember is one person as ListTeam names them: who you can hand work to.
type teamMember struct {
	ID          uint64 `json:"id"`
	DisplayName string `json:"display_name"`
}

func ( teamMember ) APIName() ( name string ) { name = "Member"; return }

type listTeamReply struct {
	Members []teamMember `json:"members"`
	Paged
}

// listTeam exists so that ordinary features -- an assignee dropdown, an owner
// picker, an @mention list -- do not need admin rights just to render a list
// of names. It returns id and display name only; role and disabled status
// stay behind the admin endpoint, because those are facts about who
// administers the app rather than about who you can hand work to.
//
// @route("GET /api/team", summary="List team",
//     tag="Account",
//     description="People with an active account, a page at a time: id and display name.",
// )
func ( handlers *Handlers ) listTeam( c fiber.Ctx , in *Page ) ( reply *listTeamReply , err error ) {
	users , err := models.ListUsers( handlers.Store )
	if err != nil { return }
	members := []teamMember{}
	for _ , user := range users {
		if user.Disabled() { continue }
		members = append( members , teamMember{ ID: user.ID , DisplayName: user.DisplayName } )
	}
	reply = &listTeamReply{}
	reply.Members , reply.Paged = cutPage( members , *in )
	return
}
