-- +goose Up
-- A new category of accounts the bot may tag: the personalities who defend
-- democracy, tagged on the tweets about the vote.
ALTER TABLE x_accounts DROP CONSTRAINT IF EXISTS x_accounts_category_check;
ALTER TABLE x_accounts ADD CONSTRAINT x_accounts_category_check
    CHECK (category IN ('party', 'outlet', 'journalist', 'factcheck', 'association', 'independent', 'personality', 'other'));

-- +goose Down
UPDATE x_accounts SET category = 'other' WHERE category = 'personality';
ALTER TABLE x_accounts DROP CONSTRAINT IF EXISTS x_accounts_category_check;
ALTER TABLE x_accounts ADD CONSTRAINT x_accounts_category_check
    CHECK (category IN ('party', 'outlet', 'journalist', 'factcheck', 'association', 'independent', 'other'));
