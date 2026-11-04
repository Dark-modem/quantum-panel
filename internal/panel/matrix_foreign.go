package panel

import "net/http"

func handleMatrixForeignRemove(w http.ResponseWriter, r *http.Request) {
	handleMatrixRemovePost(w, r)
}
