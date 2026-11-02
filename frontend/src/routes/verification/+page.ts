import { redirect } from "@sveltejs/kit";
import { eventPaths } from "$lib/event";

// The identity check belongs to the vote: it is explained on the event's
// "how it works" page now.
export const load = () => {
  redirect(308, `${eventPaths().how}#cle`);
};
