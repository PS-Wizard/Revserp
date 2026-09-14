-- name: GetMapsListingReviews :one
SELECT place_id, cid, reviews, next_page_token, fetched_at, credits_used
FROM maps_listing_reviews
WHERE place_id = $1;

-- name: UpsertMapsListingReviews :one
INSERT INTO maps_listing_reviews (place_id, cid, reviews, next_page_token, fetched_at, credits_used)
VALUES ($1, $2, $3, $4, NOW(), $5)
ON CONFLICT (place_id) DO UPDATE SET
    cid = EXCLUDED.cid,
    reviews = EXCLUDED.reviews,
    next_page_token = EXCLUDED.next_page_token,
    fetched_at = NOW(),
    credits_used = EXCLUDED.credits_used
RETURNING place_id, cid, reviews, next_page_token, fetched_at, credits_used;
