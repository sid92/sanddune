# Test image manifest (ground truth, not for model input)

| File | Description | Expected PERSON_PRESENT | Expected POURING |
|---|---|---|---|
| img01.jpg | person painting a red barrel, not pouring | YES | NO |
| img02.jpg | empty tank on truck, no person | NO | NO |
| img03.jpg | empty tank on truck, distant unrelated people | YES | NO |
| img04.jpg | person standing idle next to tank | YES | NO |
| img05.jpg | tank only, no person | NO | NO |
| img06.jpg | tarp-lined reservoir, no person (ambiguous "tank") | NO | NO |
| img07.jpg | person pouring water into a pot (any pouring counts) | YES | YES |
| img08.jpg | person pouring water into a bottle (any pouring counts) | YES | YES |
