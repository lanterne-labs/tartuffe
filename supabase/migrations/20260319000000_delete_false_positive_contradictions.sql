-- +goose Up
-- Delete 18 false positive contradictions identified by manual review.
-- IDs: 552, 553, 556, 579, 580, 581, 582, 585, 601, 603, 604, 609, 610, 611, 612, 613, 618, 619
DELETE FROM contradictions
WHERE id IN (552, 553, 556, 579, 580, 581, 582, 585, 601, 603, 604, 609, 610, 611, 612, 613, 618, 619);
