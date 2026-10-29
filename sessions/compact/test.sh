#!/bin/bash
uuids=(
    01a187f5-39a6-778d-bfeb-4b8eeb95f488
    01a187fb-3a87-7031-8581-1ff36db8bc6b
    01a18820-3328-7615-951d-da4e1708007d
    01a1882c-c40d-7389-81a4-5b00ad144cb6
    01a1885e-71e0-7517-85ce-fdecab291031
    01a1886f-9ef7-7279-94aa-6a67c9265e39
    01a18871-05e2-766f-87f9-d3af86d1ed6e
    01a18889-30e4-70d4-bcb4-83fca4efa665
    01a188a0-e565-747f-a11d-9d024a90bc74
    01a188ad-aa0e-730e-aea2-f1a0030f976b
    01a188c4-c726-71fc-a0b1-adf0f77e0a83
)
for id in "${uuids[@]}"; do
    aws lambda invoke \
    --function-name local-flint-pi-compaction \
    --region us-west-1 \
    --cli-binary-format raw-in-base64-out \
    --payload '{"session_id":"'$id'"}' \
    --log-type Tail --query LogResult --output text /tmp/out.json | base64 -d
done
